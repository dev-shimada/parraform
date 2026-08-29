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
	path, _ := cfg.Config["path"].(string)
	if path == "" {
		path = "terraform.tfstate"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.Dir, path)
	}

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
