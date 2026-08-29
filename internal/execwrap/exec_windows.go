//go:build windows

// Package execwrap runs the resolved terraform binary as a child process
// with inherited stdio, since Windows has no process-replacement syscall
// equivalent to Unix exec(2).
package execwrap

import (
	"os"
	"os/exec"
)

// Run executes bin with argv[1:] as arguments and env as its environment,
// with stdio inherited from the current process. It exits the current
// process with the child's exit code and never returns.
func Run(bin string, argv []string, env []string) error {
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
