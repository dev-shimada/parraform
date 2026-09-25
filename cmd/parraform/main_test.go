package main

import (
	"bytes"
	"context"
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
			if err := decideLockAction(&buf, mode, lockcheck.Info{}, false); err != nil {
				t.Errorf("mode %q: decideLockAction() error = %v, want nil", mode, err)
			}
			if buf.Len() != 0 {
				t.Errorf("mode %q: unexpected output on unlocked backend: %q", mode, buf.String())
			}
		}
	})

	t.Run("locked and warn prints a warning naming the holder and proceeds", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckWarn, locked, true); err != nil {
			t.Errorf("decideLockAction() error = %v, want nil", err)
		}
		if !strings.Contains(buf.String(), "alice") {
			t.Errorf("warning = %q, want it to name the holder", buf.String())
		}
	})

	t.Run("locked and warn falls back to unknown when Who is empty", func(t *testing.T) {
		var buf bytes.Buffer
		if err := decideLockAction(&buf, lockCheckWarn, lockcheck.Info{}, true); err != nil {
			t.Errorf("decideLockAction() error = %v, want nil", err)
		}
		if !strings.Contains(buf.String(), "unknown") {
			t.Errorf("warning = %q, want it to fall back to %q", buf.String(), "unknown")
		}
	})

	t.Run("locked and strict refuses without printing", func(t *testing.T) {
		var buf bytes.Buffer
		err := decideLockAction(&buf, lockCheckStrict, locked, true)
		if err == nil {
			t.Fatal("decideLockAction() error = nil, want an error refusing to run plan")
		}
		if !strings.Contains(err.Error(), "alice") {
			t.Errorf("error = %q, want it to name the holder", err.Error())
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected stderr output in strict mode: %q", buf.String())
		}
	})

	t.Run("unrecognized mode is a usage error regardless of lock state", func(t *testing.T) {
		for _, locked := range []bool{true, false} {
			var buf bytes.Buffer
			err := decideLockAction(&buf, "bogus", lockcheck.Info{}, locked)
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
		if err := decideLockAction(&buf, "", lockcheck.Info{}, false); err == nil {
			t.Error("decideLockAction() error = nil, want an invalid-mode error for an empty mode")
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
