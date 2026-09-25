// Package tfargs classifies terraform CLI invocations and computes the
// environment needed to make plan run without acquiring the state lock.
package tfargs

import "strings"

// Subcommand returns the terraform subcommand name from args (typically
// os.Args[1:]), skipping any global pre-subcommand flags such as -chdir=DIR,
// -help, -h, -version, -v. Returns "" when no subcommand is present (e.g.
// bare "terraform" or "terraform -version").
func Subcommand(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

// Chdir returns the directory passed via a leading -chdir=DIR global flag,
// or "" if none is present. Only flags preceding the subcommand are
// considered, matching terraform's own requirement that -chdir come first.
func Chdir(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			break
		}
		if v, ok := strings.CutPrefix(a, "-chdir="); ok {
			return v
		}
	}
	return ""
}

// LockCheckMode scans args for a "-lock-check" flag, accepting every form
// Go's flag package (which terraform itself is built on) recognizes:
// "-lock-check=MODE", "--lock-check=MODE", "-lock-check MODE", and
// "--lock-check MODE". Unlike -lock=BOOL, "-lock-check" is parraform's own
// invention that terraform doesn't recognize at all, so every occurrence --
// and its value, if space-separated -- is stripped from the returned args
// regardless of where it appears, mirroring how -lock=BOOL itself may
// appear anywhere among a plan's arguments (unlike -chdir, which must
// precede the subcommand).
//
// present distinguishes "not given" (mode "", present false) from "given
// with an empty or missing value" (-lock-check=, or a bare -lock-check at
// the end of args; mode "", present true), so callers can reject those
// explicitly instead of silently treating them as the unset default.
func LockCheckMode(args []string) (mode string, present bool, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if v, ok := cutLockCheckValue(a); ok {
			mode, present = v, true
			continue
		}
		if isLockCheckFlag(a) {
			present = true
			if i+1 < len(args) {
				i++
				mode = args[i]
			}
			continue
		}
		rest = append(rest, a)
	}
	return mode, present, rest
}

func cutLockCheckValue(a string) (string, bool) {
	if v, ok := strings.CutPrefix(a, "--lock-check="); ok {
		return v, true
	}
	if v, ok := strings.CutPrefix(a, "-lock-check="); ok {
		return v, true
	}
	return "", false
}

func isLockCheckFlag(a string) bool {
	return a == "-lock-check" || a == "--lock-check"
}

const planLockFalse = "-lock=false"

// PlanEnv returns env (typically os.Environ()) with -lock=false appended to
// TF_CLI_ARGS_plan, creating the variable if absent and preserving any
// existing value. Terraform inserts TF_CLI_ARGS_<cmd> immediately after the
// subcommand and applies flags last-occurrence-wins, so an explicit
// -lock=true/-lock=false later on the user's own command line still takes
// precedence over this default.
func PlanEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "TF_CLI_ARGS_plan=") {
			found = true
			e = e + " " + planLockFalse
		}
		out = append(out, e)
	}
	if !found {
		out = append(out, "TF_CLI_ARGS_plan="+planLockFalse)
	}
	return out
}
