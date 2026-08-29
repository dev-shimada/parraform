package lockcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

func testCOSObjectService(t *testing.T, srv *httptest.Server) *cos.ObjectService {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{})
	return client.Object
}

func TestCOSLockObjectKey(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		workspace string
		key       string
		want      string
	}{
		{"default workspace, no prefix", "", "default", "terraform.tfstate", "terraform.tfstate.tflock"},
		{"default workspace with prefix", "terraform/state", "default", "terraform.tfstate", "terraform/state/terraform.tfstate.tflock"},
		{"non-default workspace", "terraform/state", "staging", "terraform.tfstate", "terraform/state/staging/terraform.tfstate.tflock"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cosLockObjectKey(c.prefix, c.workspace, c.key); got != c.want {
				t.Errorf("cosLockObjectKey(%q, %q, %q) = %q, want %q", c.prefix, c.workspace, c.key, got, c.want)
			}
		})
	}
}

func TestPeekCOSLockObject_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
	}))
	defer srv.Close()

	objects := testCOSObjectService(t, srv)
	info, supported, err := peekCOSLockObject(context.Background(), objects, "terraform/state/default.tflock")
	if err != nil {
		t.Fatalf("peekCOSLockObject() error = %v", err)
	}
	if !supported {
		t.Fatal("peekCOSLockObject() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekCOSLockObject_Locked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ID":"abc-123","Who":"runner@github-actions"}`)
	}))
	defer srv.Close()

	objects := testCOSObjectService(t, srv)
	info, supported, err := peekCOSLockObject(context.Background(), objects, "terraform/state/default.tflock")
	if err != nil {
		t.Fatalf("peekCOSLockObject() error = %v", err)
	}
	if !supported {
		t.Fatal("peekCOSLockObject() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestCOSChecker_Peek_MissingBucketOrRegion(t *testing.T) {
	c := cosChecker{}
	cfg := testBackendConfig(map[string]any{"bucket": "my-bucket"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing region)")
	}
}
