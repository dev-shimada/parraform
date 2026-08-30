//go:build !windows

package lockcheck

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("local", localChecker{})
}

// localChecker peeks the local backend's lock by attempting a non-blocking
// advisory flock on the state file itself, matching how terraform's local
// backend locks it. The lock is released immediately after the attempt;
// this never holds the lock.
type localChecker struct{}

func (localChecker) Peek(_ context.Context, cfg backendcfg.Config) (Info, bool, error) {
	path := localLockTarget(cfg)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No state written yet: nothing to lock.
			return Info{}, true, nil
		}
		return Info{}, false, err
	}
	defer f.Close()

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// EWOULDBLOCK (or platform equivalent): another process holds it.
		return Info{Locked: true}, true, nil
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return Info{Locked: false}, true, nil
}

// localLockTarget resolves the absolute, workspace-qualified state file
// path from raw backend config, so the config-to-identifier wiring itself
// can be exercised directly with a backendcfg.Config, workspace included.
func localLockTarget(cfg backendcfg.Config) string {
	path, _ := cfg.Config["path"].(string)
	if path == "" {
		path = "terraform.tfstate"
	}
	path = workspaceStatePath(path, cfg.Workspace)
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.Dir, path)
	}
	return path
}

// workspaceStatePath mirrors terraform's local backend: the default
// workspace stores state at exactly the configured path, while any other
// workspace stores it under a sibling terraform.tfstate.d/<workspace>/
// directory, keeping the original path's own base filename.
func workspaceStatePath(basePath, workspace string) string {
	if workspace == "" || workspace == "default" {
		return basePath
	}
	dir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	return filepath.Join(dir, "terraform.tfstate.d", workspace, base)
}
