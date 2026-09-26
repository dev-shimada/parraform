package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dev-shimada/parraform/internal/backendcfg"
	"github.com/dev-shimada/parraform/internal/lockcheck"
)

// sleepyChecker mirrors the real-world failure mode this test guards
// against: an SDK call that ignores the context passed to it (go-tfe's
// client construction, the oss backend's ctx-less tablestore.GetRow) and
// simply blocks for a fixed duration regardless of ctx cancellation.
type sleepyChecker struct {
	sleep time.Duration
}

func (s sleepyChecker) Peek(ctx context.Context, cfg backendcfg.Config) (lockcheck.Info, bool, error) {
	time.Sleep(s.sleep)
	return lockcheck.Info{Locked: true}, true, nil
}

func TestPeekWithTimeout_BoundsCtxIgnoringChecker(t *testing.T) {
	checker := sleepyChecker{sleep: 2 * time.Second}
	budget := 100 * time.Millisecond

	start := time.Now()
	_, supported, err := peekWithTimeout(checker, backendcfg.Config{}, budget)
	elapsed := time.Since(start)

	if elapsed > budget+200*time.Millisecond {
		t.Errorf("peekWithTimeout took %v, want close to the %v budget (checker ignores ctx and sleeps for 2s)", elapsed, budget)
	}
	if err == nil {
		t.Error("err = nil, want a timeout error")
	}
	if supported {
		t.Error("supported = true on timeout, want false")
	}
}

func TestDecideLockAction(t *testing.T) {
	locked := lockcheck.Info{Who: "alice"}

	t.Run("unlocked proceeds silently in both modes", func(t *testing.T) {
		for _, mode := range []string{lockCheckWarn, lockCheckStrict} {
			var buf bytes.Buffer
			if err := decideLockAction(&buf, mode, lockcheck.Info{}, false, false); err != nil {
				t.Errorf("mode %q: decideLockAction() error = %v, want nil", mode, err)
			}
			if buf.Len() != 0 {
				t.Errorf("mode %q: unexpected output on unlocked backend: %q", mode, buf.String())
			}
		}
	})

	t.Run("locked and warn prints a warning naming the holder and proceeds", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckWarn, locked, true, false); err != nil {
			t.Errorf("decideLockAction() error = %v, want nil", err)
		}
		if !strings.Contains(buf.String(), "alice") {
			t.Errorf("warning = %q, want it to name the holder", buf.String())
		}
		if !strings.Contains(buf.String(), "running plan unlocked") {
			t.Errorf("warning = %q, want it to say plan will run unlocked", buf.String())
		}
	})

	t.Run("locked, warn, and an explicit -lock=true describes the actual outcome instead", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckWarn, locked, true, true); err != nil {
			t.Errorf("decideLockAction() error = %v, want nil", err)
		}
		if strings.Contains(buf.String(), "running plan unlocked") {
			t.Errorf("warning = %q, must not claim plan runs unlocked when -lock=true is explicit", buf.String())
		}
		if !strings.Contains(buf.String(), "-lock=true") {
			t.Errorf("warning = %q, want it to mention the explicit -lock=true", buf.String())
		}
	})

	t.Run("locked and warn falls back to unknown when Who is empty", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckWarn, lockcheck.Info{}, true, false); err != nil {
			t.Errorf("decideLockAction() error = %v, want nil", err)
		}
		if !strings.Contains(buf.String(), "unknown") {
			t.Errorf("warning = %q, want it to fall back to %q", buf.String(), "unknown")
		}
	})

	t.Run("locked and strict refuses without printing, regardless of an explicit -lock=true", func(t *testing.T) {
		for _, explicitLockTrue := range []bool{false, true} {
			var buf bytes.Buffer
			err := decideLockAction(&buf, lockCheckStrict, locked, true, explicitLockTrue)
			if err == nil {
				t.Fatalf("explicitLockTrue=%v: decideLockAction() error = nil, want an error refusing to run plan", explicitLockTrue)
			}
			if !strings.Contains(err.Error(), "alice") {
				t.Errorf("explicitLockTrue=%v: error = %q, want it to name the holder", explicitLockTrue, err.Error())
			}
			if buf.Len() != 0 {
				t.Errorf("explicitLockTrue=%v: unexpected stderr output in strict mode: %q", explicitLockTrue, buf.String())
			}
		}
	})

	t.Run("unrecognized mode is a usage error regardless of lock state", func(t *testing.T) {
		for _, locked := range []bool{true, false} {
			var buf bytes.Buffer
			err := decideLockAction(&buf, "bogus", lockcheck.Info{}, locked, false)
			if err == nil {
				t.Fatalf("locked=%v: decideLockAction() error = nil, want an invalid-mode error", locked)
			}
			if !strings.Contains(err.Error(), "bogus") {
				t.Errorf("locked=%v: error = %q, want it to name the bad value", locked, err.Error())
			}
		}
	})

	t.Run("empty mode is a usage error", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, "", lockcheck.Info{}, false, false); err == nil {
			t.Error("decideLockAction() error = nil, want an invalid-mode error for an empty mode")
		}
	})
}

func TestResolveLockCheckMode(t *testing.T) {
	t.Run("argv flag wins over env", func(t *testing.T) {
		mode, present, rest := resolveLockCheckMode(
			[]string{"plan", "-lock-check=strict"},
			[]string{"TF_CLI_ARGS_plan=-lock-check=warn"},
		)
		if mode != "strict" || !present {
			t.Errorf("resolveLockCheckMode() = (%q, %v), want (%q, true)", mode, present, "strict")
		}
		if !reflect.DeepEqual(rest, []string{"plan"}) {
			t.Errorf("rest = %v, want the flag stripped", rest)
		}
	})

	t.Run("falls back to env when argv doesn't have the flag", func(t *testing.T) {
		mode, present, _ := resolveLockCheckMode(
			[]string{"plan"},
			[]string{"TF_CLI_ARGS_plan=-lock-check=strict"},
		)
		if mode != "strict" || !present {
			t.Errorf("resolveLockCheckMode() = (%q, %v), want (%q, true)", mode, present, "strict")
		}
	})

	t.Run("neither given", func(t *testing.T) {
		mode, present, _ := resolveLockCheckMode([]string{"plan"}, []string{"PATH=/bin"})
		if mode != "" || present {
			t.Errorf("resolveLockCheckMode() = (%q, %v), want (\"\", false)", mode, present)
		}
	})
}

// TestCheckLock exercises checkLock's own wiring -- mode defaulting,
// LockOverride detection, and error propagation -- as opposed to the
// backend peek itself (covered by peekWithTimeout above, and for real by
// the e2e suite). peekLock always observes locked=false here: it resolves
// the backend config relative to os.Getwd(), and this package's own source
// directory has no .terraform/terraform.tfstate cache file, so
// backendcfg.Discover finds nothing regardless of argv.
func TestCheckLock(t *testing.T) {
	t.Run("default mode with nothing locked proceeds silently", func(t *testing.T) {
		var buf bytes.Buffer
		if err := checkLock(&buf, []string{"-lock-timeout=0s"}, "", false); err != nil {
			t.Errorf("checkLock() error = %v, want nil", err)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected output: %q", buf.String())
		}
	})

	t.Run("an invalid mode is still a usage error even when nothing is locked", func(t *testing.T) {
		var buf bytes.Buffer
		err := checkLock(&buf, nil, "bogus", true)
		if err == nil {
			t.Fatal("checkLock() error = nil, want an invalid-mode error")
		}
		if !strings.Contains(err.Error(), "bogus") {
			t.Errorf("error = %q, want it to name the bad value", err.Error())
		}
	})

	t.Run("an explicit -lock=true in argv doesn't itself cause an error or output on an unlocked backend", func(t *testing.T) {
		var buf bytes.Buffer
		if err := checkLock(&buf, []string{"-lock=true"}, "", false); err != nil {
			t.Errorf("checkLock() error = %v, want nil", err)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected output on an unlocked backend: %q", buf.String())
		}
	})
}

func TestPeekWithTimeout_ReturnsPromptlyWhenCheckerIsFast(t *testing.T) {
	checker := sleepyChecker{sleep: 0}
	info, supported, err := peekWithTimeout(checker, backendcfg.Config{}, time.Second)
	if err != nil {
		t.Fatalf("peekWithTimeout() error = %v", err)
	}
	if !supported {
		t.Error("supported = false, want true")
	}
	if !info.Locked {
		t.Error("info.Locked = false, want true")
	}
}
