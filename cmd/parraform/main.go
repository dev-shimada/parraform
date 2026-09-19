// Command parraform is a transparent wrapper around the terraform CLI that
// runs "plan" without acquiring the state lock, so parallel plans in CI
// don't fight over it, while leaving "apply" and every other command's
// locking behavior untouched. See DESIGN.md for the full rationale.
package main

import (
	"context"
	"fmt"
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
// that for "plan" it injects TF_CLI_ARGS_plan=-lock=false. It does not
// return on success.
func runTerraform(argv []string) error {
	bin, err := tfbin.Resolve()
	if err != nil {
		return err
	}

	env := os.Environ()
	if tfargs.Subcommand(argv) == "plan" {
		warnIfLocked(argv)
		env = tfargs.PlanEnv(env)
	}

	return execwrap.Run(bin, append([]string{bin}, argv...), env)
}

// defaultLockCheckTimeout bounds how long warnIfLocked will wait on a
// backend peek (credential resolution for a cloud SDK's default chain can
// itself take seconds, e.g. IMDS probing or DefaultAzureCredential walking
// its fallback list) before giving up silently. This is real latency added
// to every "plan" invocation, which cuts against the whole point of this
// tool, so it's kept short and deliberately tunable via
// PARRAFORM_LOCK_CHECK_TIMEOUT (a Go duration string; 0 or negative
// disables the check entirely) rather than hardcoded without an escape
// hatch.
const defaultLockCheckTimeout = 3 * time.Second

// warnIfLocked performs a best-effort, read-only peek at the configured
// backend's lock and prints a warning if it's currently held. It never
// blocks plan and any failure here is silently ignored: the check is purely
// informational, not a gate. This means a timeout or a peek error is
// indistinguishable from "unlocked" to the user — an accepted trade-off,
// see DESIGN.md.
func warnIfLocked(argv []string) {
	timeout := defaultLockCheckTimeout
	if raw := os.Getenv("PARRAFORM_LOCK_CHECK_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			timeout = d
		}
	}
	if timeout <= 0 {
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
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
	if err != nil || cfg == nil {
		return
	}

	checker, ok := lockcheck.For(cfg.Type)
	if !ok {
		return
	}

	info, supported, err := peekWithTimeout(checker, *cfg, timeout)
	if err != nil || !supported || !info.Locked {
		return
	}

	who := info.Who
	if who == "" {
		who = "unknown"
	}
	fmt.Fprintf(os.Stderr,
		"parraform: warning: state lock is currently held (holder: %s) — running plan unlocked against a possibly-changing state\n",
		who)
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
