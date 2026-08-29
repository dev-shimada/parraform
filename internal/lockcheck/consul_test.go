package lockcheck

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	consulapi "github.com/hashicorp/consul/api"
)

func testConsulKV(t *testing.T, srv *httptest.Server) consulKVGetter {
	t.Helper()
	config := consulapi.DefaultConfig()
	config.Address = srv.URL
	client, err := consulapi.NewClient(config)
	if err != nil {
		t.Fatalf("consulapi.NewClient() error = %v", err)
	}
	return client.KV()
}

func TestConsulStatePath(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		workspace string
		want      string
	}{
		{"default workspace unchanged", "terraform/state", "default", "terraform/state"},
		{"empty workspace treated as default", "terraform/state", "", "terraform/state"},
		{"non-default workspace", "terraform/state", "staging", "terraform/state-env:staging"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := consulStatePath(c.path, c.workspace); got != c.want {
				t.Errorf("consulStatePath(%q, %q) = %q, want %q", c.path, c.workspace, got, c.want)
			}
		})
	}
}

func TestPeekConsulLockInfo_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	kv := testConsulKV(t, srv)
	info, supported, err := peekConsulLockInfo(context.Background(), kv, "terraform/state")
	if err != nil {
		t.Fatalf("peekConsulLockInfo() error = %v", err)
	}
	if !supported {
		t.Fatal("peekConsulLockInfo() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekConsulLockInfo_Locked(t *testing.T) {
	val := base64.StdEncoding.EncodeToString([]byte(`{"ID":"abc-123","Who":"runner@github-actions"}`))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, `[{"LockIndex":1,"Key":"terraform/state/.lockinfo","Flags":0,"Value":"%s","CreateIndex":10,"ModifyIndex":10}]`, val)
	}))
	defer srv.Close()

	kv := testConsulKV(t, srv)
	info, supported, err := peekConsulLockInfo(context.Background(), kv, "terraform/state")
	if err != nil {
		t.Fatalf("peekConsulLockInfo() error = %v", err)
	}
	if !supported {
		t.Fatal("peekConsulLockInfo() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestConsulChecker_Peek_LockDisabled(t *testing.T) {
	c := consulChecker{}
	cfg := testBackendConfig(map[string]any{"path": "terraform/state", "lock": false})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (lock=false means terraform never locks)")
	}
}

func TestConsulChecker_Peek_MissingPath(t *testing.T) {
	c := consulChecker{}
	cfg := testBackendConfig(map[string]any{})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing path)")
	}
}
