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
		_, _ = fmt.Fprintf(w, `{"data":{"id":"ws-abc123","type":"workspaces","attributes":{"name":"prod","locked":%v}}}`, locked)
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

func TestTFELockTarget(t *testing.T) {
	t.Run("missing organization", func(t *testing.T) {
		cfg := testBackendConfigWithType("remote", map[string]any{}, "default")
		_, _, _, _, ok := tfeLockTarget(cfg)
		if ok {
			t.Error("tfeLockTarget() ok = true, want false (missing organization)")
		}
	})

	t.Run("missing token", func(t *testing.T) {
		t.Setenv("TF_TOKEN_app_terraform_io", "")
		cfg := testBackendConfigWithType("remote", map[string]any{
			"organization": "my-org",
			"workspaces":   map[string]any{"name": "prod"},
		}, "default")
		_, _, _, _, ok := tfeLockTarget(cfg)
		if ok {
			t.Error("tfeLockTarget() ok = true, want false (no token available)")
		}
	})

	t.Run("hostname defaults, token from config attribute", func(t *testing.T) {
		t.Setenv("TF_TOKEN_app_terraform_io", "")
		cfg := testBackendConfigWithType("remote", map[string]any{
			"organization": "my-org",
			"workspaces":   map[string]any{"name": "prod"},
			"token":        "attr-token",
		}, "default")
		hostname, org, workspace, token, ok := tfeLockTarget(cfg)
		if !ok {
			t.Fatal("tfeLockTarget() ok = false, want true")
		}
		if hostname != "app.terraform.io" || org != "my-org" || workspace != "prod" || token != "attr-token" {
			t.Errorf("tfeLockTarget() = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
				hostname, org, workspace, token, "app.terraform.io", "my-org", "prod", "attr-token")
		}
	})

	t.Run("custom hostname, TF_TOKEN env var takes precedence over attribute", func(t *testing.T) {
		// tfeToken only replaces dots with underscores (hyphens are left
		// as-is); this is the documented gap versus terraform's own
		// double-underscore hyphen encoding.
		t.Setenv("TF_TOKEN_my-tfe_example_com", "env-token")
		cfg := testBackendConfigWithType("remote", map[string]any{
			"organization": "my-org",
			"hostname":     "my-tfe.example.com",
			"workspaces":   map[string]any{"name": "prod"},
			"token":        "attr-token",
		}, "default")
		hostname, _, _, token, ok := tfeLockTarget(cfg)
		if !ok {
			t.Fatal("tfeLockTarget() ok = false, want true")
		}
		if hostname != "my-tfe.example.com" || token != "env-token" {
			t.Errorf("tfeLockTarget() = (%q, token=%q), want (%q, token=%q)", hostname, token, "my-tfe.example.com", "env-token")
		}
	})
}

type backendcfgTestConfig struct {
	Type      string
	Config    map[string]any
	Workspace string
}
