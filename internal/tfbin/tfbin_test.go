package tfbin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolve_EnvOverride(t *testing.T) {
	t.Setenv(envOverride, "/custom/path/terraform")
	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got != "/custom/path/terraform" {
		t.Errorf("Resolve() = %q, want /custom/path/terraform", got)
	}
}

func TestResolve_FindsOnPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, exeName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envOverride, "")
	t.Setenv("PATH", dir)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got != bin {
		t.Errorf("Resolve() = %q, want %q", got, bin)
	}
}

func TestResolve_SkipsSelfSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink shim scenario is unix-specific")
	}

	// Simulate: parraform is installed as ./realdir/parraform, and a CI
	// shim symlinks ./shimdir/terraform -> ./realdir/parraform, with
	// shimdir first on PATH. Resolve must skip the shim (itself) and fall
	// through to the real terraform binary later on PATH.
	realDir := t.TempDir()
	shimDir := t.TempDir()
	realTFDir := t.TempDir()

	parraformBin := filepath.Join(realDir, "parraform")
	if err := os.WriteFile(parraformBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	shimPath := filepath.Join(shimDir, "terraform")
	if err := os.Symlink(parraformBin, shimPath); err != nil {
		t.Fatal(err)
	}

	realTF := filepath.Join(realTFDir, "terraform")
	if err := os.WriteFile(realTF, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	origExecutable := executableOverride
	executableOverride = func() (string, error) { return parraformBin, nil }
	defer func() { executableOverride = origExecutable }()

	t.Setenv(envOverride, "")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+realTFDir)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got != realTF {
		t.Errorf("Resolve() = %q, want %q (should skip self-symlink shim)", got, realTF)
	}
}
