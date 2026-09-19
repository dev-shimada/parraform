package lockcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

func testGCSClient(t *testing.T, srv *httptest.Server) *storage.Client {
	t.Helper()
	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("storage.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestGCSLockObject(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		workspace string
		want      string
	}{
		{"default workspace, no prefix", "", "default", "default.tflock"},
		{"default workspace with prefix", "terraform/state", "default", "terraform/state/default.tflock"},
		{"non-default workspace", "terraform/state", "staging", "terraform/state/staging.tflock"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gcsLockObject(c.prefix, c.workspace); got != c.want {
				t.Errorf("gcsLockObject(%q, %q) = %q, want %q", c.prefix, c.workspace, got, c.want)
			}
		})
	}
}

func TestPeekGCSLockfile_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
	}))
	defer srv.Close()

	client := testGCSClient(t, srv)
	obj := client.Bucket("my-bucket").Object("terraform/state/default.tflock")

	info, supported, err := peekGCSLockfile(context.Background(), obj)
	if err != nil {
		t.Fatalf("peekGCSLockfile() error = %v", err)
	}
	if !supported {
		t.Fatal("peekGCSLockfile() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekGCSLockfile_Locked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"ID":"abc-123","Who":"runner@github-actions"}`)
	}))
	defer srv.Close()

	client := testGCSClient(t, srv)
	obj := client.Bucket("my-bucket").Object("terraform/state/default.tflock")

	info, supported, err := peekGCSLockfile(context.Background(), obj)
	if err != nil {
		t.Fatalf("peekGCSLockfile() error = %v", err)
	}
	if !supported {
		t.Fatal("peekGCSLockfile() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestGCSLockTarget(t *testing.T) {
	cases := []struct {
		name       string
		cfg        map[string]any
		workspace  string
		wantBucket string
		wantObject string
		wantOK     bool
	}{
		{"missing bucket", map[string]any{"prefix": "terraform/state"}, "default", "", "", false},
		{"default workspace", map[string]any{"bucket": "my-bucket", "prefix": "terraform/state"}, "default", "my-bucket", "terraform/state/default.tflock", true},
		{"empty workspace treated as default", map[string]any{"bucket": "my-bucket", "prefix": "terraform/state"}, "", "my-bucket", "terraform/state/default.tflock", true},
		{"non-default workspace", map[string]any{"bucket": "my-bucket", "prefix": "terraform/state"}, "staging", "my-bucket", "terraform/state/staging.tflock", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType("gcs", c.cfg, c.workspace)
			bucket, object, ok := gcsLockTarget(cfg)
			if ok != c.wantOK {
				t.Fatalf("gcsLockTarget() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if bucket != c.wantBucket || object != c.wantObject {
				t.Errorf("gcsLockTarget() = (%q, %q), want (%q, %q)", bucket, object, c.wantBucket, c.wantObject)
			}
		})
	}
}

func TestGCSChecker_Peek_MissingBucket(t *testing.T) {
	c := gcsChecker{}
	cfg := testBackendConfig(map[string]any{"prefix": "terraform/state"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing bucket)")
	}
}
