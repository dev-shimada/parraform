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
