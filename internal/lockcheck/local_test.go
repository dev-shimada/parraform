//go:build !windows

package lockcheck

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func TestLocalLockTarget(t *testing.T) {
	cases := []struct {
		name      string
		cfg       map[string]any
		dir       string
		workspace string
		want      string
	}{
		{"default path and workspace", map[string]any{}, "/work", "default", "/work/terraform.tfstate"},
		{"explicit relative path", map[string]any{"path": "state/terraform.tfstate"}, "/work", "default", "/work/state/terraform.tfstate"},
		{"non-default workspace", map[string]any{}, "/work", "staging", "/work/terraform.tfstate.d/staging/terraform.tfstate"},
		{"absolute path ignores dir", map[string]any{"path": "/abs/terraform.tfstate"}, "/work", "default", "/abs/terraform.tfstate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := backendcfg.Config{Type: "local", Config: c.cfg, Dir: c.dir, Workspace: c.workspace}
			if got := localLockTarget(cfg); got != c.want {
				t.Errorf("localLockTarget() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLocalChecker_Peek_NotLocked and TestLocalChecker_Peek_Locked exercise
// localChecker.Peek end to end -- the real flock syscall on a real file --
// unlike every other backend in this package, which stops at the two
// disconnected layers of "config produces the right identifier" and
// "the SDK response is interpreted correctly". This is trivially possible
// here because the local backend needs no SDK or mocked server: a second,
// independently-opened file descriptor holding a real flock is exactly
// what "another process holds the lock" looks like.
func TestLocalChecker_Peek_NotLocked(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "terraform.tfstate")
	if err := os.WriteFile(statePath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := localChecker{}
	cfg := backendcfg.Config{Type: "local", Config: map[string]any{}, Dir: dir, Workspace: "default"}
	info, supported, err := c.Peek(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if !supported {
		t.Fatal("Peek() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestLocalChecker_Peek_Locked(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "terraform.tfstate")
	if err := os.WriteFile(statePath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hold a real exclusive flock via a second, independently-opened file
	// descriptor -- flock locks are per open-file-description, so this
	// genuinely conflicts with the fd Peek() will open internally, the
	// same way a concurrent terraform process holding the lock would.
	holder, err := os.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("failed to acquire holder flock: %v", err)
	}
	defer func() { _ = syscall.Flock(int(holder.Fd()), syscall.LOCK_UN) }()

	c := localChecker{}
	cfg := backendcfg.Config{Type: "local", Config: map[string]any{}, Dir: dir, Workspace: "default"}
	info, supported, err := c.Peek(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if !supported {
		t.Fatal("Peek() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
}

func TestWorkspaceStatePath(t *testing.T) {
	cases := []struct {
		name      string
		basePath  string
		workspace string
		want      string
	}{
		{"default workspace unchanged", "terraform.tfstate", "default", "terraform.tfstate"},
		{"empty workspace treated as default", "terraform.tfstate", "", "terraform.tfstate"},
		{"non-default workspace", "terraform.tfstate", "staging", "terraform.tfstate.d/staging/terraform.tfstate"},
		{"non-default workspace with directory in path", "state/terraform.tfstate", "staging", "state/terraform.tfstate.d/staging/terraform.tfstate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workspaceStatePath(c.basePath, c.workspace); got != c.want {
				t.Errorf("workspaceStatePath(%q, %q) = %q, want %q", c.basePath, c.workspace, got, c.want)
			}
		})
	}
}
