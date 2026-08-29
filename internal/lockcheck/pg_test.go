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
