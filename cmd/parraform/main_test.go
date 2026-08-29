package main

import (
	"context"
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
