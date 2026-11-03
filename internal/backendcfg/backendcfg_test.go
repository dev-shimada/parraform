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
	if cfg == nil {
		t.Fatal("Discover() returned nil config")
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
	if cfg == nil {
		t.Fatal("Discover() returned nil config")
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
	if cfg == nil {
		t.Fatal("Discover() returned nil config")
	}
	if cfg.Workspace != "production" {
		t.Errorf("Workspace = %q, want %q (TF_WORKSPACE must win over the environment file)", cfg.Workspace, "production")
	}
}

func TestDiscover_TFDataDir(t *testing.T) {
	// The default .terraform names one backend and the TF_DATA_DIR one
	// another: Discover must follow TF_DATA_DIR, and never fall back to
	// the stale default.
	setup := func(t *testing.T) (dir, custom string) {
		t.Helper()
		dir = t.TempDir()
		writeCacheFile(t, dir, `{"backend":{"type":"gcs","config":{}}}`)

		custom = filepath.Join(dir, "custom-data")
		if err := os.MkdirAll(custom, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(custom, "terraform.tfstate"), []byte(`{"backend":{"type":"s3","config":{}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(custom, "environment"), []byte("staging"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TF_WORKSPACE", "")
		return dir, custom
	}

	t.Run("absolute path", func(t *testing.T) {
		dir, custom := setup(t)
		t.Setenv("TF_DATA_DIR", custom)

		cfg, err := Discover(dir)
		if err != nil || cfg == nil {
			t.Fatalf("Discover() = (%+v, %v), want a config", cfg, err)
		}
		if cfg.Type != "s3" || cfg.Workspace != "staging" {
			t.Errorf("Discover() = type %q, workspace %q, want s3 / staging from TF_DATA_DIR", cfg.Type, cfg.Workspace)
		}
	})

	t.Run("relative path resolves against dir", func(t *testing.T) {
		dir, _ := setup(t)
		t.Setenv("TF_DATA_DIR", "custom-data")

		cfg, err := Discover(dir)
		if err != nil || cfg == nil {
			t.Fatalf("Discover() = (%+v, %v), want a config", cfg, err)
		}
		if cfg.Type != "s3" {
			t.Errorf("Type = %q, want s3 from the relative TF_DATA_DIR", cfg.Type)
		}
	})

	t.Run("a TF_DATA_DIR without a cache is not backed by the stale .terraform", func(t *testing.T) {
		dir, _ := setup(t)
		t.Setenv("TF_DATA_DIR", filepath.Join(dir, "empty"))

		cfg, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if cfg != nil {
			t.Errorf("Discover() = %+v, want nil: the default .terraform must not be used when TF_DATA_DIR is set", cfg)
		}
	})
}
