package lockcheck

import "github.com/dev-shimada/parraform/internal/backendcfg"

func testBackendConfig(cfg map[string]any) backendcfg.Config {
	return backendcfg.Config{Config: cfg}
}
