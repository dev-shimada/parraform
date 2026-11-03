// Command parraform is a transparent wrapper around the terraform CLI that
// runs "plan" without acquiring the state lock, so parallel plans in CI
// don't fight over it, while leaving "apply" and every other command's
// locking behavior untouched. See DESIGN.md for the full rationale.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dev-shimada/parraform/internal/backendcfg"
	"github.com/dev-shimada/parraform/internal/cli"
	"github.com/dev-shimada/parraform/internal/execwrap"
	"github.com/dev-shimada/parraform/internal/lockcheck"
	"github.com/dev-shimada/parraform/internal/tfargs"
	"github.com/dev-shimada/parraform/internal/tfbin"
)

func main() {
	root := cli.NewRootCommand(func() []string { return os.Args[1:] }, runTerraform)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "parraform:", err)
		os.Exit(1)
	}
}

// runTerraform execs the real terraform binary with argv untouched, except
// that for "plan" it injects TF_CLI_ARGS_plan=-lock=false and strips its own
// "-lock-check" flag (see LockCheckMode) before terraform ever sees argv --
// terraform has no such flag and would reject it. -lock-check may also be
// given via TF_CLI_ARGS_plan itself (see LockCheckModeFromPlanEnv), for
// callers running parraform as a drop-in "terraform" that can't add CLI
// flags directly (e.g. under Atlantis or terragrunt); an explicit argv flag
// always takes precedence over that fallback. It does not return on
// success.
func runTerraform(argv []string) error {
	bin, err := tfbin.Resolve()
	if err != nil {
		return err
	}

	env := os.Environ()
	if tfargs.Subcommand(argv) == "plan" {
		mode, present, rest := resolveLockCheckMode(argv, env)
		argv = rest
		if err := checkLock(os.Stderr, argv, mode, present); err != nil {
			return err
		}
		env = tfargs.PlanEnv(env)
	}

	return execwrap.Run(bin, append([]string{bin}, argv...), env)
}

// resolveLockCheckMode determines plan's effective -lock-check mode: an
// explicit argv flag (see tfargs.LockCheckMode) always wins; when absent, it
// falls back to one found inside env's TF_CLI_ARGS_plan (see
// tfargs.LockCheckModeFromPlanEnv), for callers running parraform as a
// drop-in "terraform" that can't add CLI flags directly (e.g. under
// Atlantis or terragrunt). rest is argv with the flag stripped, if it was
// there at all -- terraform must never see it either way it arrived.
func resolveLockCheckMode(argv []string, env []string) (mode string, present bool, rest []string) {
	mode, present, rest = tfargs.LockCheckMode(argv)
	if !present {
		mode, present = tfargs.LockCheckModeFromPlanEnv(env)
	}
	return mode, present, rest
}

// defaultLockCheckTimeout bounds how long peekLock will wait on a
// backend peek (credential resolution for a cloud SDK's default chain can
// itself take seconds, e.g. IMDS probing or DefaultAzureCredential walking
// its fallback list) before giving up silently. This is real latency added
// to every "plan" invocation, which cuts against the whole point of this
// tool, so it's kept short and deliberately tunable via
// PARRAFORM_LOCK_CHECK_TIMEOUT (a Go duration string; 0 or negative
// disables the check entirely) rather than hardcoded without an escape
// hatch.
const defaultLockCheckTimeout = 3 * time.Second

// lockCheckWarn, lockCheckStrict and lockCheckRequire are the -lock-check
// modes. warn (the default, used when the flag isn't given) prints a warning
// to stderr and proceeds; strict refuses to run plan at all when the lock is
// confirmed held. Neither ever blocks on mere uncertainty in the peek itself
// (unsupported backend, timeout, peek error) -- that is the project's
// fail-open default. require is the fail-closed variant of strict: it also
// refuses when the peek could not confirm the lock is free, so a plan can
// only proceed once the lock has positively been seen unheld.
const (
	lockCheckWarn    = "warn"
	lockCheckStrict  = "strict"
	lockCheckRequire = "require"
)

// lockState is the outcome of a peek. The zero value is lockUnknown on
// purpose: a peek that forgot to say anything must never read as "free".
type lockState int

const (
	// lockUnknown: the peek could not tell (see lockStatus.reason).
	lockUnknown lockState = iota
	// lockFree: the peek positively observed that the lock is not held.
	lockFree
	// lockHeld: the peek positively observed a held lock.
	lockHeld
)

// lockStatus is peekLock's result. info is only meaningful for lockHeld,
// and reason only for lockUnknown, where it says why the peek could not
// confirm anything so -lock-check=require can surface it.
type lockStatus struct {
	state  lockState
	info   lockcheck.Info
	reason string
}

func unknownLock(format string, args ...any) lockStatus {
	return lockStatus{state: lockUnknown, reason: fmt.Sprintf(format, args...)}
}

// checkLock is runTerraform's plan-only lock gate: it peeks at the backend
// lock (see peekLock) and turns the result into actual behavior via
// decideLockAction. present distinguishes an explicit-but-empty
// "-lock-check=" from the flag not being given at all, so the former is
// rejected instead of silently defaulting to warn.
func checkLock(w io.Writer, argv []string, mode string, present bool) error {
	if mode == "" && !present {
		mode = lockCheckWarn
	}
	explicitLock, lockValue := tfargs.LockOverride(argv)
	return decideLockAction(w, mode, peekLock(argv), explicitLock && lockValue)
}

// decideLockAction turns a peek result into runTerraform's actual behavior.
// A confirmed lock (lockHeld) prints a warning and proceeds under
// lockCheckWarn, or refuses to run plan under lockCheckStrict and
// lockCheckRequire. lockFree always proceeds silently. lockUnknown
// proceeds silently under warn and strict (fail-open) but refuses under
// require, naming the reason. An unrecognized (including empty) mode is a
// usage error, checked before consulting the peek result at all so a typo
// is never masked by an unlocked backend.
//
// explicitLockTrue is true when the plan's own arguments set an explicit
// -lock=true (see tfargs.LockOverride), which wins over PlanEnv's injected
// -lock=false, so terraform will actually attempt to acquire the lock
// itself once parraform lets plan proceed. The warn-mode message adjusts
// for this: claiming "running plan unlocked" would be wrong in that case.
func decideLockAction(w io.Writer, mode string, st lockStatus, explicitLockTrue bool) error {
	if mode != lockCheckWarn && mode != lockCheckStrict && mode != lockCheckRequire {
		return fmt.Errorf("invalid -lock-check value %q (want %q, %q or %q)", mode, lockCheckWarn, lockCheckStrict, lockCheckRequire)
	}

	switch st.state {
	case lockFree:
		return nil
	case lockHeld:
		// handled below
	default:
		if mode == lockCheckRequire {
			return fmt.Errorf("could not confirm the state lock is free: %s; refusing to run plan (-lock-check=require)", st.reason)
		}
		return nil
	}

	who := st.info.Who
	if who == "" {
		who = "unknown"
	}

	if mode == lockCheckStrict || mode == lockCheckRequire {
		return fmt.Errorf("state lock is currently held (holder: %s); refusing to run plan (-lock-check=%s)", who, mode)
	}

	situation := "running plan unlocked against a possibly-changing state"
	if explicitLockTrue {
		situation = "plan was run with an explicit -lock=true, so terraform will attempt to acquire the lock itself"
	}
	_, _ = fmt.Fprintf(w, "parraform: warning: state lock is currently held (holder: %s) — %s\n", who, situation)
	return nil
}

// peekLock performs a best-effort, read-only peek at the configured
// backend's lock. It reports lockHeld or lockFree only when the peek
// definitively observed one; any uncertainty (missing cache file,
// unsupported backend, timeout, or peek error) is lockUnknown with a
// reason. Whether that blocks plan is decideLockAction's call: only
// -lock-check=require does, matching the project's fail-open default
// otherwise.
func peekLock(argv []string) lockStatus {
	timeout := defaultLockCheckTimeout
	if raw := os.Getenv("PARRAFORM_LOCK_CHECK_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			timeout = d
		}
	}
	if timeout <= 0 {
		return unknownLock("the lock check is disabled (PARRAFORM_LOCK_CHECK_TIMEOUT=%s)", os.Getenv("PARRAFORM_LOCK_CHECK_TIMEOUT"))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return unknownLock("resolving the working directory: %v", err)
	}

	dir := cwd
	if chdir := tfargs.Chdir(argv); chdir != "" {
		if filepath.IsAbs(chdir) {
			dir = chdir
		} else {
			dir = filepath.Join(cwd, chdir)
		}
	}

	cfg, err := backendcfg.Discover(dir)
	if err != nil {
		return unknownLock("reading the backend config in %s: %v", dir, err)
	}
	if cfg == nil {
		return unknownLock("no initialized backend found in %s (.terraform/terraform.tfstate is missing or names no backend; run init first)", dir)
	}

	checker, ok := lockcheck.For(cfg.Type)
	if !ok {
		return unknownLock("backend %q is not supported by the lock check", cfg.Type)
	}

	result, supported, err := peekWithTimeout(checker, *cfg, timeout)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return unknownLock("checking the %s backend lock timed out after %s (raise PARRAFORM_LOCK_CHECK_TIMEOUT)", cfg.Type, timeout)
	case err != nil:
		return unknownLock("checking the %s backend lock failed: %v", cfg.Type, err)
	case !supported:
		return unknownLock("the %s backend's lock cannot be checked with its current configuration (e.g. s3 with neither dynamodb_table nor use_lockfile)", cfg.Type)
	case result.Locked:
		return lockStatus{state: lockHeld, info: result}
	}
	return lockStatus{state: lockFree}
}

// peekWithTimeout enforces timeout even when checker.Peek doesn't honor
// ctx internally. Some backend SDKs make blocking calls that ignore the
// context passed to them entirely -- go-tfe's client construction does a
// synchronous, ctx-less GET /api/v2/ping with its own retry/backoff, and
// the oss backend's tablestore SDK's GetRow takes no context parameter at
// all -- so relying solely on ctx cancellation inside Peek does not
// actually bound those two checkers to PARRAFORM_LOCK_CHECK_TIMEOUT. This
// wraps the call in a goroutine and races it against the timeout instead.
//
// If the goroutine doesn't finish in time, it's abandoned rather than
// waited on: the process is about to exec() the real terraform binary
// (replacing this process image entirely on Unix), so there is nothing
// to clean up and no leaked resource that outlives the parraform process.
func peekWithTimeout(checker lockcheck.Checker, cfg backendcfg.Config, timeout time.Duration) (lockcheck.Info, bool, error) {
	type result struct {
		info      lockcheck.Info
		supported bool
		err       error
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ch := make(chan result, 1)
	go func() {
		info, supported, err := checker.Peek(ctx, cfg)
		ch <- result{info, supported, err}
	}()

	select {
	case r := <-ch:
		return r.info, r.supported, r.err
	case <-ctx.Done():
		return lockcheck.Info{}, false, ctx.Err()
	}
}
