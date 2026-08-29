package lockcheck

import "github.com/dev-shimada/parraform/internal/backendcfg"

func testBackendConfig(cfg map[string]any) backendcfg.Config {
	return backendcfg.Config{Config: cfg}
}

func testBackendConfigWithType(backendType string, cfg map[string]any, workspace string) backendcfg.Config {
	return backendcfg.Config{Type: backendType, Config: cfg, Workspace: workspace}
}
