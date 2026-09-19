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

func TestOSSLockTarget(t *testing.T) {
	cases := []struct {
		name             string
		cfg              map[string]any
		workspace        string
		wantTable        string
		wantInstanceName string
		wantEndpoint     string
		wantLockPath     string
		wantOK           bool
	}{
		{"tablestore_table not configured", map[string]any{"bucket": "my-bucket"}, "default", "", "", "", "", false},
		{"missing bucket", map[string]any{"tablestore_table": "locks"}, "default", "", "", "", "", false},
		{
			"missing instance/endpoint",
			map[string]any{"tablestore_table": "locks", "bucket": "my-bucket"}, "default",
			"", "", "", "", false,
		},
		{
			"default workspace, defaults applied",
			map[string]any{
				"tablestore_table":         "locks",
				"bucket":                   "my-bucket",
				"tablestore_instance_name": "my-instance",
				"tablestore_endpoint":      "https://my-instance.cn-hangzhou.ots.aliyuncs.com",
			},
			"default",
			"locks", "my-instance", "https://my-instance.cn-hangzhou.ots.aliyuncs.com", "my-bucket/env:/terraform.tfstate", true,
		},
		{
			"non-default workspace, custom prefix/key",
			map[string]any{
				"tablestore_table":         "locks",
				"bucket":                   "my-bucket",
				"tablestore_instance_name": "my-instance",
				"tablestore_endpoint":      "https://my-instance.cn-hangzhou.ots.aliyuncs.com",
				"prefix":                   "custom",
				"key":                      "custom.tfstate",
			},
			"staging",
			"locks", "my-instance", "https://my-instance.cn-hangzhou.ots.aliyuncs.com", "my-bucket/custom/staging/custom.tfstate", true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType("oss", c.cfg, c.workspace)
			table, instanceName, endpoint, lockPath, ok := ossLockTarget(cfg)
			if ok != c.wantOK {
				t.Fatalf("ossLockTarget() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if table != c.wantTable || instanceName != c.wantInstanceName || endpoint != c.wantEndpoint || lockPath != c.wantLockPath {
				t.Errorf("ossLockTarget() = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					table, instanceName, endpoint, lockPath, c.wantTable, c.wantInstanceName, c.wantEndpoint, c.wantLockPath)
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
