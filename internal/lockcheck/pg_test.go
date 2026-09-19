package lockcheck

import "testing"

func TestPgQuoteIdent(t *testing.T) {
	cases := []struct {
		name  string
		ident string
		want  string
	}{
		{"simple identifier", "terraform_remote_state", `"terraform_remote_state"`},
		{"embedded double quote is escaped", `weird"schema`, `"weird""schema"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pgQuoteIdent(c.ident); got != c.want {
				t.Errorf("pgQuoteIdent(%q) = %q, want %q", c.ident, got, c.want)
			}
		})
	}
}

func TestPgLockTarget(t *testing.T) {
	t.Run("missing conn_str, no env fallback", func(t *testing.T) {
		t.Setenv("PG_CONN_STR", "")
		cfg := testBackendConfigWithType("pg", map[string]any{"schema_name": "terraform_remote_state"}, "default")
		_, _, _, ok := pgLockTarget(cfg)
		if ok {
			t.Error("pgLockTarget() ok = true, want false (no conn_str, no PG_CONN_STR)")
		}
	})

	t.Run("conn_str and schema from config, workspace resolved", func(t *testing.T) {
		t.Setenv("PG_CONN_STR", "")
		t.Setenv("PG_SCHEMA_NAME", "")
		cfg := testBackendConfigWithType("pg", map[string]any{
			"conn_str":    "postgres://x/y",
			"schema_name": "my_schema",
		}, "staging")
		connStr, schema, workspace, ok := pgLockTarget(cfg)
		if !ok {
			t.Fatal("pgLockTarget() ok = false, want true")
		}
		if connStr != "postgres://x/y" || schema != "my_schema" || workspace != "staging" {
			t.Errorf("pgLockTarget() = (%q, %q, %q), want (%q, %q, %q)", connStr, schema, workspace, "postgres://x/y", "my_schema", "staging")
		}
	})

	t.Run("conn_str and schema fall back to env vars, workspace defaults", func(t *testing.T) {
		t.Setenv("PG_CONN_STR", "postgres://env/db")
		t.Setenv("PG_SCHEMA_NAME", "env_schema")
		cfg := testBackendConfigWithType("pg", map[string]any{}, "")
		connStr, schema, workspace, ok := pgLockTarget(cfg)
		if !ok {
			t.Fatal("pgLockTarget() ok = false, want true")
		}
		if connStr != "postgres://env/db" || schema != "env_schema" || workspace != "default" {
			t.Errorf("pgLockTarget() = (%q, %q, %q), want (%q, %q, %q)", connStr, schema, workspace, "postgres://env/db", "env_schema", "default")
		}
	})

	t.Run("schema defaults to terraform_remote_state when unset everywhere", func(t *testing.T) {
		t.Setenv("PG_CONN_STR", "")
		t.Setenv("PG_SCHEMA_NAME", "")
		cfg := testBackendConfigWithType("pg", map[string]any{"conn_str": "postgres://x/y"}, "default")
		_, schema, _, ok := pgLockTarget(cfg)
		if !ok {
			t.Fatal("pgLockTarget() ok = false, want true")
		}
		if schema != "terraform_remote_state" {
			t.Errorf("schema = %q, want %q", schema, "terraform_remote_state")
		}
	})
}

func TestPgChecker_Peek_MissingConnStr(t *testing.T) {
	c := pgChecker{}
	cfg := testBackendConfig(map[string]any{"schema_name": "terraform_remote_state"})
	_, supported, err := c.Peek(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing conn_str)")
	}
}

func TestPgChecker_Peek_ConnStrFromEnv(t *testing.T) {
	// conn_str omitted from the backend config (the PG_CONN_STR pattern):
	// Peek must not bail out early as "unsupported" just because it's
	// absent from cfg.Config -- it should attempt the env var and only
	// then proceed to actually connect (and fail, since there's no real
	// postgres here; what matters is it got past the "unsupported" gate).
	// 127.0.0.1:1 fails fast with connection refused, no DNS lookup and
	// no dependency on network access being available in this environment.
	t.Setenv("PG_CONN_STR", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
	c := pgChecker{}
	cfg := testBackendConfig(map[string]any{})
	_, supported, err := c.Peek(t.Context(), cfg)
	if err == nil {
		t.Fatal("Peek() error = nil, want a connection error (no real postgres at this address)")
	}
	if supported {
		t.Error("Peek() supported = true on a hard connection error, want false")
	}
}
