package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	held := lockStatus{state: lockHeld, info: lockcheck.Info{Who: "alice"}}
	free := lockStatus{state: lockFree}
	unknown := lockStatus{state: lockUnknown, reason: `backend "foo" is not supported by the lock check`}

	t.Run("a confirmed-free lock proceeds silently in every mode", func(t *testing.T) {
		for _, mode := range []string{lockCheckWarn, lockCheckStrict, lockCheckRequire} {
			var buf bytes.Buffer
			if err := decideLockAction(&buf, mode, free, false); err != nil {
				t.Errorf("mode %q: decideLockAction() error = %v, want nil", mode, err)
			}
			if buf.Len() != 0 {
				t.Errorf("mode %q: unexpected output on unlocked backend: %q", mode, buf.String())
			}
		}
	})

	t.Run("an undetermined lock proceeds silently in warn and strict (fail-open)", func(t *testing.T) {
		for _, mode := range []string{lockCheckWarn, lockCheckStrict} {
			var buf bytes.Buffer
			if err := decideLockAction(&buf, mode, unknown, false); err != nil {
				t.Errorf("mode %q: decideLockAction() error = %v, want nil", mode, err)
			}
			if buf.Len() != 0 {
				t.Errorf("mode %q: unexpected output on an undetermined lock: %q", mode, buf.String())
			}
		}
	})

	t.Run("an undetermined lock refuses in require, naming the reason", func(t *testing.T) {
		var buf bytes.Buffer
		err := decideLockAction(&buf, lockCheckRequire, unknown, false)
		if err == nil {
			t.Fatal("decideLockAction() error = nil, want an error refusing to run plan")
		}
		if !strings.Contains(err.Error(), unknown.reason) {
			t.Errorf("error = %q, want it to include the reason %q", err.Error(), unknown.reason)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected stderr output in require mode: %q", buf.String())
		}
	})

	t.Run("the zero lockStatus is undetermined, never free", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckRequire, lockStatus{}, false); err == nil {
			t.Error("decideLockAction() error = nil, want require to refuse a zero-value status")
		}
	})

	t.Run("locked and warn prints a warning naming the holder and proceeds", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckWarn, held, false); err != nil {
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
		if err := decideLockAction(&buf, lockCheckWarn, held, true); err != nil {
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
		if err := decideLockAction(&buf, lockCheckWarn, lockStatus{state: lockHeld}, false); err != nil {
			t.Errorf("decideLockAction() error = %v, want nil", err)
		}
		if !strings.Contains(buf.String(), "unknown") {
			t.Errorf("warning = %q, want it to fall back to %q", buf.String(), "unknown")
		}
	})

	t.Run("locked and strict/require refuse without printing, regardless of an explicit -lock=true", func(t *testing.T) {
		for _, mode := range []string{lockCheckStrict, lockCheckRequire} {
			for _, explicitLockTrue := range []bool{false, true} {
				var buf bytes.Buffer
				err := decideLockAction(&buf, mode, held, explicitLockTrue)
				if err == nil {
					t.Fatalf("mode %q, explicitLockTrue=%v: decideLockAction() error = nil, want an error refusing to run plan", mode, explicitLockTrue)
				}
				if !strings.Contains(err.Error(), "alice") {
					t.Errorf("mode %q, explicitLockTrue=%v: error = %q, want it to name the holder", mode, explicitLockTrue, err.Error())
				}
				if !strings.Contains(err.Error(), "-lock-check="+mode) {
					t.Errorf("mode %q, explicitLockTrue=%v: error = %q, want it to name the mode", mode, explicitLockTrue, err.Error())
				}
				if buf.Len() != 0 {
					t.Errorf("mode %q, explicitLockTrue=%v: unexpected stderr output: %q", mode, explicitLockTrue, buf.String())
				}
			}
		}
	})

	t.Run("unrecognized mode is a usage error regardless of lock state", func(t *testing.T) {
		for _, st := range []lockStatus{held, free, unknown} {
			var buf bytes.Buffer
			err := decideLockAction(&buf, "bogus", st, false)
			if err == nil {
				t.Fatalf("state=%v: decideLockAction() error = nil, want an invalid-mode error", st.state)
			}
			if !strings.Contains(err.Error(), "bogus") {
				t.Errorf("state=%v: error = %q, want it to name the bad value", st.state, err.Error())
			}
		}
	})

	t.Run("empty mode is a usage error", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, "", free, false); err == nil {
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
// the e2e suite, and TestPeekLock below). peekLock never observes a held
// lock here: it resolves the backend config relative to os.Getwd(), and
// this package's own source directory has no .terraform/terraform.tfstate
// cache file, so backendcfg.Discover finds nothing regardless of argv --
// which is "undetermined", so only -lock-check=require objects.
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

// stubChecker is a lockcheck.Checker with a canned Peek result, registered
// under a throwaway backend type so TestPeekLock can drive every branch of
// peekLock without a real backend.
type stubChecker struct {
	info      lockcheck.Info
	supported bool
	err       error
}

func (s stubChecker) Peek(context.Context, backendcfg.Config) (lockcheck.Info, bool, error) {
	return s.info, s.supported, s.err
}

func TestPeekLock(t *testing.T) {
	lockcheck.Register("stub-free", stubChecker{supported: true})
	lockcheck.Register("stub-held", stubChecker{info: lockcheck.Info{Locked: true, Who: "alice"}, supported: true})
	lockcheck.Register("stub-unsupported", stubChecker{supported: false})
	lockcheck.Register("stub-error", stubChecker{supported: true, err: errors.New("AccessDenied")})
	lockcheck.Register("stub-timeout", stubChecker{supported: true, err: context.DeadlineExceeded})

	// backendDir returns a directory whose .terraform/terraform.tfstate
	// names the given backend type, for use via -chdir.
	backendDir := func(t *testing.T, backendType string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
			t.Fatal(err)
		}
		cache := `{"version":3,"backend":{"type":"` + backendType + `","config":{}}}`
		if err := os.WriteFile(filepath.Join(dir, ".terraform", "terraform.tfstate"), []byte(cache), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	tests := []struct {
		name       string
		setup      func(t *testing.T) []string // returns argv
		wantState  lockState
		wantReason string // substring, for lockUnknown
	}{
		{
			name:      "the backend reports the lock free",
			setup:     func(t *testing.T) []string { return []string{"-chdir=" + backendDir(t, "stub-free"), "plan"} },
			wantState: lockFree,
		},
		{
			name:      "the backend reports the lock held",
			setup:     func(t *testing.T) []string { return []string{"-chdir=" + backendDir(t, "stub-held"), "plan"} },
			wantState: lockHeld,
		},
		{
			name:       "no cache file (uninitialized, or an implicit local backend)",
			setup:      func(t *testing.T) []string { return []string{"-chdir=" + t.TempDir(), "plan"} },
			wantState:  lockUnknown,
			wantReason: "no initialized backend",
		},
		{
			name: "an unreadable cache file",
			setup: func(t *testing.T) []string {
				dir := backendDir(t, "x")
				if err := os.WriteFile(filepath.Join(dir, ".terraform", "terraform.tfstate"), []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
				return []string{"-chdir=" + dir, "plan"}
			},
			wantState:  lockUnknown,
			wantReason: "reading the backend config",
		},
		{
			name:       "a backend type with no checker",
			setup:      func(t *testing.T) []string { return []string{"-chdir=" + backendDir(t, "no-such-backend"), "plan"} },
			wantState:  lockUnknown,
			wantReason: `backend "no-such-backend" is not supported`,
		},
		{
			name:       "a supported backend whose lock is not configured",
			setup:      func(t *testing.T) []string { return []string{"-chdir=" + backendDir(t, "stub-unsupported"), "plan"} },
			wantState:  lockUnknown,
			wantReason: "cannot be checked",
		},
		{
			name:       "the peek itself fails",
			setup:      func(t *testing.T) []string { return []string{"-chdir=" + backendDir(t, "stub-error"), "plan"} },
			wantState:  lockUnknown,
			wantReason: "AccessDenied",
		},
		{
			name:       "the peek times out",
			setup:      func(t *testing.T) []string { return []string{"-chdir=" + backendDir(t, "stub-timeout"), "plan"} },
			wantState:  lockUnknown,
			wantReason: "PARRAFORM_LOCK_CHECK_TIMEOUT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PARRAFORM_LOCK_CHECK_TIMEOUT", "")
			got := peekLock(tt.setup(t))
			if got.state != tt.wantState {
				t.Fatalf("state = %v (reason %q), want %v", got.state, got.reason, tt.wantState)
			}
			if tt.wantState == lockUnknown && !strings.Contains(got.reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.reason, tt.wantReason)
			}
			if tt.wantState == lockHeld && got.info.Who != "alice" {
				t.Errorf("info.Who = %q, want %q", got.info.Who, "alice")
			}
		})
	}

	t.Run("a disabled check (timeout <= 0) is undetermined, not free", func(t *testing.T) {
		t.Setenv("PARRAFORM_LOCK_CHECK_TIMEOUT", "0")
		got := peekLock([]string{"-chdir=" + backendDir(t, "stub-free"), "plan"})
		if got.state != lockUnknown || !strings.Contains(got.reason, "disabled") {
			t.Errorf("peekLock() = %+v, want lockUnknown mentioning the check is disabled", got)
		}
	})
}

func TestCheckLock_Require(t *testing.T) {
	lockcheck.Register("stub-require-free", stubChecker{supported: true})
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := `{"version":3,"backend":{"type":"stub-require-free","config":{}}}`
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "terraform.tfstate"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PARRAFORM_LOCK_CHECK_TIMEOUT", "")

	t.Run("proceeds when the lock is confirmed free", func(t *testing.T) {
		var buf bytes.Buffer
		if err := checkLock(&buf, []string{"-chdir=" + dir, "plan"}, lockCheckRequire, true); err != nil {
			t.Errorf("checkLock() error = %v, want nil", err)
		}
	})

	t.Run("refuses when no backend can be found", func(t *testing.T) {
		var buf bytes.Buffer
		err := checkLock(&buf, []string{"-chdir=" + t.TempDir(), "plan"}, lockCheckRequire, true)
		if err == nil || !strings.Contains(err.Error(), "-lock-check=require") {
			t.Errorf("checkLock() error = %v, want a -lock-check=require refusal", err)
		}
	})

	t.Run("PARRAFORM_LOCK_CHECK_TIMEOUT=0 cannot be used to bypass it", func(t *testing.T) {
		t.Setenv("PARRAFORM_LOCK_CHECK_TIMEOUT", "0")
		var buf bytes.Buffer
		if err := checkLock(&buf, []string{"-chdir=" + dir, "plan"}, lockCheckRequire, true); err == nil {
			t.Error("checkLock() error = nil, want a refusal when the check is disabled")
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
