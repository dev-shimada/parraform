// Package lockcheck performs read-only "peek" checks of a terraform state
// backend's lock, without ever acquiring it. Checkers never call the
// backend's actual Lock/Unlock operation; a peek is purely informational
// and must never block or fail the plan it's checking.
package lockcheck

import (
	"context"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

// Info describes what a peek observed.
type Info struct {
	Locked bool
	// Who identifies the lock holder when the backend exposes it
	// (e.g. terraform's "Who" field, or a TFC run URL). May be empty
	// even when Locked is true.
	Who string
}

// Checker performs a read-only lock peek for one backend type.
type Checker interface {
	// Peek inspects the current lock state. supported=false means this
	// backend configuration can't be peeked (missing permissions,
	// unrecognized config shape, etc.); callers should treat that the
	// same as "no checker registered" and skip the check silently.
	Peek(ctx context.Context, cfg backendcfg.Config) (info Info, supported bool, err error)
}

var registry = map[string]Checker{}

// Register adds a Checker for the given backend type ("s3", "gcs", "local",
// "remote", ...). Intended to be called from init() in each backend's file.
func Register(backendType string, c Checker) {
	registry[backendType] = c
}

// For returns the registered Checker for a backend type, if any.
func For(backendType string) (Checker, bool) {
	c, ok := registry[backendType]
	return c, ok
}
