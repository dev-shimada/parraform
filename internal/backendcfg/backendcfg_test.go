package backendcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCacheFile(t *testing.T, dir, content string) {
	t.Helper()
	tfDir := filepath.Join(dir, ".terraform")
	if err := os.MkdirAll(tfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tfDir, "terraform.tfstate"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover_NoCacheFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if cfg != nil {
		t.Errorf("Discover() = %+v, want nil (no .terraform/terraform.tfstate written)", cfg)
	}
}

func TestDiscover_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, `{not valid json`)
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("Discover() error = nil, want a parse error")
	}
}

func TestDiscover_EmptyBackendType(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, `{"version":3,"backend":{"type":"","config":{}}}`)
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if cfg != nil {
		t.Errorf("Discover() = %+v, want nil (empty backend.type)", cfg)
	}
}

func TestDiscover_PopulatesTypeConfigAndDir(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, `{
		"version": 3,
		"backend": {
			"type": "gcs",
			"config": {"bucket": "my-bucket", "prefix": "terraform/state"}
		}
	}`)

	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if cfg == nil {
		t.Fatal("Discover() = nil, want a populated Config")
	}
	if cfg.Type != "gcs" {
		t.Errorf("Type = %q, want %q", cfg.Type, "gcs")
	}
	if cfg.Dir != dir {
		t.Errorf("Dir = %q, want %q", cfg.Dir, dir)
	}
	if got, _ := cfg.Config["bucket"].(string); got != "my-bucket" {
		t.Errorf(`Config["bucket"] = %q, want "my-bucket"`, got)
	}
	if got, _ := cfg.Config["prefix"].(string); got != "terraform/state" {
		t.Errorf(`Config["prefix"] = %q, want "terraform/state"`, got)
	}
}

func TestDiscover_WorkspaceDefaultWhenEnvironmentFileAbsent(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, `{"backend":{"type":"local","config":{}}}`)
	t.Setenv("TF_WORKSPACE", "")

	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if cfg.Workspace != "default" {
		t.Errorf("Workspace = %q, want %q (no .terraform/environment file)", cfg.Workspace, "default")
	}
}

func TestDiscover_WorkspaceFromEnvironmentFile(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, `{"backend":{"type":"local","config":{}}}`)
	t.Setenv("TF_WORKSPACE", "")
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "environment"), []byte("staging\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if cfg.Workspace != "staging" {
		t.Errorf("Workspace = %q, want %q (trimmed .terraform/environment content)", cfg.Workspace, "staging")
	}
}

func TestDiscover_TFWorkspaceEnvOverridesEnvironmentFile(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, `{"backend":{"type":"local","config":{}}}`)
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "environment"), []byte("staging"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_WORKSPACE", "production")

	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if cfg.Workspace != "production" {
		t.Errorf("Workspace = %q, want %q (TF_WORKSPACE must win over the environment file)", cfg.Workspace, "production")
	}
}
