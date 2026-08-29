package lockcheck

import (
	"runtime"
	"testing"
)

// TestRegistry_ClaimedBackendsAreReachable guards against the failure mode
// where a Checker's Peek logic is fully tested in isolation but its
// Register("type", ...) key has a typo or doesn't match the string
// terraform actually writes as backend.type — which would make that
// backend silently never get checked via the real Discover -> For(cfg.Type)
// path, with every existing test still green.
func TestRegistry_ClaimedBackendsAreReachable(t *testing.T) {
	want := []string{"s3", "gcs", "azurerm", "remote", "cloud", "consul", "kubernetes", "pg", "cos", "oci", "oss"}
	if runtime.GOOS != "windows" {
		// local's Peek uses syscall.Flock and only builds on !windows.
		want = append(want, "local")
	}

	for _, backendType := range want {
		t.Run(backendType, func(t *testing.T) {
			if _, ok := For(backendType); !ok {
				t.Errorf("For(%q) not registered; check the string passed to Register() in this backend's init()", backendType)
			}
		})
	}
}
