// Package tfargs classifies terraform CLI invocations and computes the
// environment needed to make plan run without acquiring the state lock.
package tfargs

import (
	"strconv"
	"strings"
)

const tfCLIArgsPlanPrefix = "TF_CLI_ARGS_plan="

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

// LockCheckModeFromPlanEnv looks for a "-lock-check" flag (see LockCheckMode
// for the forms accepted) inside env's existing TF_CLI_ARGS_plan value, if
// any. This exists for callers that run parraform as a drop-in "terraform"
// replacement (e.g. under Atlantis or terragrunt) and so can only pass
// extra plan flags through that env var, not argv. It never modifies env;
// PlanEnv separately strips the matched flag out of the value it actually
// writes; terraform must never see "-lock-check" however it arrived.
func LockCheckModeFromPlanEnv(env []string) (mode string, present bool) {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, tfCLIArgsPlanPrefix); ok {
			mode, present, _ = lockCheckModeFromPlanArgsValue(v)
			return mode, present
		}
	}
	return "", false
}

// planArgsToken is one whitespace-delimited token from a TF_CLI_ARGS_plan
// value, found by splitPlanArgsPreservingQuotes.
type planArgsToken struct {
	text string
}

// splitPlanArgsPreservingQuotes splits s on whitespace like strings.Fields,
// except that single- and double-quoted regions suppress splitting inside
// them, so a quoted argument containing a space (e.g. `-var-file="a
// b.tfvars"`) comes back as one token instead of two. This mirrors, well
// enough for token-boundary purposes, the relevant subset of terraform's
// own splitting of this same env var (mattn/go-shellwords, as used by
// mergeEnvArgs in terraform's main.go, with ParseEnv and ParseBacktick both
// disabled there). Quote characters are kept as part of the token text, not
// stripped or otherwise interpreted -- this is boundary detection only.
func splitPlanArgsPreservingQuotes(s string) []planArgsToken {
	var tokens []planArgsToken
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		var quote byte
		for i < len(s) {
			c := s[i]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
			} else if c == '\'' || c == '"' {
				quote = c
			} else if c == ' ' || c == '\t' {
				break
			}
			i++
		}
		tokens = append(tokens, planArgsToken{text: s[start:i]})
	}
	return tokens
}

// lockCheckModeFromPlanArgsValue scans a TF_CLI_ARGS_plan-shaped raw string
// for a "-lock-check" flag and returns its value, whether it was present,
// and the string with just that flag's token(s) removed. When no such flag
// is found, rest is s itself, unmodified -- so a caller building on top of
// this (see PlanEnv) never changes behavior for a value that doesn't
// contain "-lock-check" at all, including one with quoted arguments that
// splitPlanArgsPreservingQuotes's tokenization would otherwise be at risk
// of mangling.
func lockCheckModeFromPlanArgsValue(s string) (mode string, present bool, rest string) {
	tokens := splitPlanArgsPreservingQuotes(s)
	keep := make([]bool, len(tokens))
	for i := range keep {
		keep[i] = true
	}

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i].text
		if v, ok := cutLockCheckValue(tok); ok {
			mode, present = v, true
			keep[i] = false
			continue
		}
		if isLockCheckFlag(tok) {
			present = true
			keep[i] = false
			if i+1 < len(tokens) {
				i++
				mode = tokens[i].text
				keep[i] = false
			}
			continue
		}
	}

	if !present {
		return "", false, s
	}

	kept := make([]string, 0, len(tokens))
	for i, t := range tokens {
		if keep[i] {
			kept = append(kept, t.text)
		}
	}
	return mode, true, strings.Join(kept, " ")
}

const planLockFalse = "-lock=false"

// PlanEnv returns env (typically os.Environ()) with -lock=false appended to
// TF_CLI_ARGS_plan, creating the variable if absent and preserving any
// existing value -- except that a "-lock-check" flag found in that existing
// value (see LockCheckModeFromPlanEnv) is stripped out first, since
// terraform has no such flag and would reject it; a value with no such
// flag is carried through byte-for-byte unmodified. Terraform inserts
// TF_CLI_ARGS_<cmd> immediately after the subcommand and applies flags
// last-occurrence-wins, so an explicit -lock=true/-lock=false later on the
// user's own command line still takes precedence over this default.
func PlanEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, tfCLIArgsPlanPrefix); ok {
			found = true
			_, _, rest := lockCheckModeFromPlanArgsValue(v)
			e = tfCLIArgsPlanPrefix + rest + " " + planLockFalse
		}
		out = append(out, e)
	}
	if !found {
		out = append(out, tfCLIArgsPlanPrefix+planLockFalse)
	}
	return out
}

// LockOverride reports whether args explicitly sets -lock (or --lock),
// either as "-lock=BOOL"/"--lock=BOOL" or as a bare "-lock"/"--lock" (Go's
// flag package, which terraform itself is built on, never takes a bool
// flag's value from a separate following argument, so a bare occurrence
// always means true). Like -lock=BOOL itself, it may appear anywhere among
// the arguments; the last occurrence wins, matching terraform's own
// flag-parsing precedence. explicit is false, and value meaningless, when
// -lock isn't present at all or every occurrence has an unparseable value.
//
// This exists only so checkLock's warning text can say what will actually
// happen: PlanEnv's injected -lock=false always loses to an explicit
// -lock=true given directly on the command line (though not to one hidden
// inside TF_CLI_ARGS_plan -- see PlanEnv), so claiming "running plan
// unlocked" in that case would be wrong. It intentionally only scans argv,
// never env, for that reason.
func LockOverride(args []string) (explicit bool, value bool) {
	for _, a := range args {
		if v, ok := cutLockValue(a); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				explicit, value = true, b
			}
			continue
		}
		if a == "-lock" || a == "--lock" {
			explicit, value = true, true
		}
	}
	return explicit, value
}

func cutLockValue(a string) (string, bool) {
	if v, ok := strings.CutPrefix(a, "--lock="); ok {
		return v, true
	}
	if v, ok := strings.CutPrefix(a, "-lock="); ok {
		return v, true
	}
	return "", false
}
