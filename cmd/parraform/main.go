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

// warnIfLocked performs a best-effort, read-only peek at the configured
// backend's lock and prints a warning if it's currently held. It never
// blocks plan and any failure here is silently ignored: the check is purely
// informational, not a gate.
func warnIfLocked(argv []string) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	info, supported, err := checker.Peek(ctx, *cfg)
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
