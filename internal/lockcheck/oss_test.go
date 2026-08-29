package lockcheck

import (
	"context"
	"testing"
)

func TestOSSStateFile(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		workspace string
		key       string
		want      string
	}{
		{"default workspace", "env:", "default", "terraform.tfstate", "env:/terraform.tfstate"},
		{"empty workspace treated as default", "env:", "", "terraform.tfstate", "env:/terraform.tfstate"},
		{"non-default workspace", "env:", "staging", "terraform.tfstate", "env:/staging/terraform.tfstate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ossStateFile(c.prefix, c.workspace, c.key); got != c.want {
				t.Errorf("ossStateFile(%q, %q, %q) = %q, want %q", c.prefix, c.workspace, c.key, got, c.want)
			}
		})
	}
}

func TestOSSChecker_Peek_LockingNotConfigured(t *testing.T) {
	c := ossChecker{}
	cfg := testBackendConfig(map[string]any{"bucket": "my-bucket"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (tablestore_table not configured, so terraform never locks)")
	}
}

func TestOSSChecker_Peek_MissingBucket(t *testing.T) {
	c := ossChecker{}
	cfg := testBackendConfig(map[string]any{"tablestore_table": "locks"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing bucket)")
	}
}
