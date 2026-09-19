//go:build !windows

// Package execwrap replaces the current process with the resolved terraform
// binary so stdio, TTY detection, signal handling, and the exit code are
// byte-for-byte identical to invoking terraform directly.
package execwrap

import "syscall"

// Run replaces the current process image with bin, running with argv and
// env. On success it never returns.
func Run(bin string, argv []string, env []string) error {
	return syscall.Exec(bin, argv, env)
}
