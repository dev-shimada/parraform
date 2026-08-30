package lockcheck

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// testOCIClient generates a fresh, ephemeral RSA key purely to satisfy the
// SDK's PEM parsing when constructing a client for these tests; it never
// signs a request against anything but the local httptest server and is
// discarded when the test ends, so there's no static key material to
// embed in source (which secret scanners understandably flag even for an
// intentionally-throwaway key).
func testOCIClient(t *testing.T, srv *httptest.Server) *objectstorage.ObjectStorageClient {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	provider := common.NewRawConfigurationProvider("tenancy", "user", "us-ashburn-1", "fingerprint", string(pemKey), nil)
	client, err := objectstorage.NewObjectStorageClientWithConfigurationProvider(provider)
	if err != nil {
		t.Fatalf("NewObjectStorageClientWithConfigurationProvider() error = %v", err)
	}
	client.Host = srv.URL
	return &client
}

func TestOCILockObjectName(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		workspace string
		key       string
		want      string
	}{
		{"default workspace, bare key", "tf-state-env", "default", "terraform.tfstate", "terraform.tfstate.lock"},
		{"non-default workspace", "tf-state-env", "staging", "terraform.tfstate", "tf-state-env/staging/terraform.tfstate.lock"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ociLockObjectName(c.prefix, c.workspace, c.key); got != c.want {
				t.Errorf("ociLockObjectName(%q, %q, %q) = %q, want %q", c.prefix, c.workspace, c.key, got, c.want)
			}
		})
	}
}

func TestPeekOCILockObject_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"code":"ObjectNotFound","message":"The object does not exist"}`)
	}))
	defer srv.Close()

	client := testOCIClient(t, srv)
	info, supported, err := peekOCILockObject(context.Background(), client, "myns", "mybucket", "terraform.tfstate.lock")
	if err != nil {
		t.Fatalf("peekOCILockObject() error = %v", err)
	}
	if !supported {
		t.Fatal("peekOCILockObject() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekOCILockObject_Locked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ID":"abc-123","Who":"runner@github-actions"}`)
	}))
	defer srv.Close()

	client := testOCIClient(t, srv)
	info, supported, err := peekOCILockObject(context.Background(), client, "myns", "mybucket", "terraform.tfstate.lock")
	if err != nil {
		t.Fatalf("peekOCILockObject() error = %v", err)
	}
	if !supported {
		t.Fatal("peekOCILockObject() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestOCILockTarget(t *testing.T) {
	cases := []struct {
		name          string
		cfg           map[string]any
		workspace     string
		wantBucket    string
		wantNamespace string
		wantObject    string
		wantOK        bool
	}{
		{"missing namespace", map[string]any{"bucket": "mybucket"}, "default", "", "", "", false},
		{
			"default workspace, default key/prefix",
			map[string]any{"bucket": "mybucket", "namespace": "myns"}, "default",
			"mybucket", "myns", "terraform.tfstate.lock", true,
		},
		{
			"non-default workspace, custom key/prefix",
			map[string]any{"bucket": "mybucket", "namespace": "myns", "key": "custom.tfstate", "workspace_key_prefix": "custom-prefix"}, "staging",
			"mybucket", "myns", "custom-prefix/staging/custom.tfstate.lock", true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType("oci", c.cfg, c.workspace)
			bucket, namespace, object, ok := ociLockTarget(cfg)
			if ok != c.wantOK {
				t.Fatalf("ociLockTarget() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if bucket != c.wantBucket || namespace != c.wantNamespace || object != c.wantObject {
				t.Errorf("ociLockTarget() = (%q, %q, %q), want (%q, %q, %q)", bucket, namespace, object, c.wantBucket, c.wantNamespace, c.wantObject)
			}
		})
	}
}

func TestOCIChecker_Peek_MissingBucketOrNamespace(t *testing.T) {
	c := ociChecker{}
	cfg := testBackendConfig(map[string]any{"bucket": "mybucket"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing namespace)")
	}
}
