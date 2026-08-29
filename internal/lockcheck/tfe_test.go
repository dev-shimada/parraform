package lockcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	tfe "github.com/hashicorp/go-tfe"
)

func testTFEWorkspaces(t *testing.T, locked bool) tfeWorkspaceReader {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"data":{"id":"ws-abc123","type":"workspaces","attributes":{"name":"prod","locked":%v}}}`, locked)
	}))
	t.Cleanup(srv.Close)

	client, err := tfe.NewClient(&tfe.Config{Address: srv.URL, Token: "dummy-token"})
	if err != nil {
		t.Fatalf("tfe.NewClient() error = %v", err)
	}
	return client.Workspaces
}

func TestPeekTFEWorkspaceLock_NotLocked(t *testing.T) {
	r := testTFEWorkspaces(t, false)
	info, supported, err := peekTFEWorkspaceLock(context.Background(), r, "my-org", "prod")
	if err != nil {
		t.Fatalf("peekTFEWorkspaceLock() error = %v", err)
	}
	if !supported {
		t.Fatal("peekTFEWorkspaceLock() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekTFEWorkspaceLock_Locked(t *testing.T) {
	r := testTFEWorkspaces(t, true)
	info, supported, err := peekTFEWorkspaceLock(context.Background(), r, "my-org", "prod")
	if err != nil {
		t.Fatalf("peekTFEWorkspaceLock() error = %v", err)
	}
	if !supported {
		t.Fatal("peekTFEWorkspaceLock() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
}

func TestTFEWorkspaceName(t *testing.T) {
	cases := []struct {
		name       string
		backendCfg backendcfgTestConfig
		want       string
		wantOK     bool
	}{
		{
			"remote backend, name mode, map shape",
			backendcfgTestConfig{
				Type:      "remote",
				Config:    map[string]any{"workspaces": map[string]any{"name": "prod"}},
				Workspace: "default",
			},
			"prod", true,
		},
		{
			"remote backend, name mode, list shape",
			backendcfgTestConfig{
				Type:      "remote",
				Config:    map[string]any{"workspaces": []any{map[string]any{"name": "prod"}}},
				Workspace: "default",
			},
			"prod", true,
		},
		{
			"remote backend, prefix mode",
			backendcfgTestConfig{
				Type:      "remote",
				Config:    map[string]any{"workspaces": map[string]any{"prefix": "app-"}},
				Workspace: "staging",
			},
			"app-staging", true,
		},
		{
			"remote backend, prefix mode, workspace already prefixed",
			backendcfgTestConfig{
				Type:      "remote",
				Config:    map[string]any{"workspaces": map[string]any{"prefix": "app-"}},
				Workspace: "app-staging",
			},
			"app-staging", true,
		},
		{
			"remote backend, prefix mode, default workspace is invalid",
			backendcfgTestConfig{
				Type:      "remote",
				Config:    map[string]any{"workspaces": map[string]any{"prefix": "app-"}},
				Workspace: "default",
			},
			"", false,
		},
		{
			"cloud block, name mode",
			backendcfgTestConfig{
				Type:      "cloud",
				Config:    map[string]any{"workspaces": map[string]any{"name": "prod"}},
				Workspace: "default",
			},
			"prod", true,
		},
		{
			"cloud block, tags mode unsupported",
			backendcfgTestConfig{
				Type:      "cloud",
				Config:    map[string]any{"workspaces": map[string]any{"tags": []any{"app"}}},
				Workspace: "default",
			},
			"", false,
		},
		{
			"missing workspaces block",
			backendcfgTestConfig{
				Type:      "remote",
				Config:    map[string]any{},
				Workspace: "default",
			},
			"", false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType(c.backendCfg.Type, c.backendCfg.Config, c.backendCfg.Workspace)
			got, ok := tfeWorkspaceName(cfg)
			if ok != c.wantOK || got != c.want {
				t.Errorf("tfeWorkspaceName() = (%q, %v), want (%q, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

type backendcfgTestConfig struct {
	Type      string
	Config    map[string]any
	Workspace string
}
