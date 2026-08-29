// Package tfbin locates the real terraform binary to delegate to, taking
// care not to recurse into parraform itself when it is installed under the
// name "terraform" (e.g. via a symlink shim in CI).
package tfbin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const envOverride = "PARRAFORM_TERRAFORM_BIN"

// executableOverride allows tests to fake os.Executable().
var executableOverride = os.Executable

func exeName() string {
	if runtime.GOOS == "windows" {
		return "terraform.exe"
	}
	return "terraform"
}

// Resolve returns the absolute path to the terraform binary to exec.
func Resolve() (string, error) {
	if p := os.Getenv(envOverride); p != "" {
		return p, nil
	}

	self := ""
	if exe, err := executableOverride(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			self = resolved
		} else {
			self = exe
		}
	}

	name := exeName()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}

		resolved := candidate
		if r, err := filepath.EvalSymlinks(candidate); err == nil {
			resolved = r
		}
		if self != "" && resolved == self {
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("terraform binary not found in PATH (set %s to override)", envOverride)
}
