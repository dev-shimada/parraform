package lockcheck

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
)

func testAzureBlobClient(t *testing.T, srv *httptest.Server, container, blobName string) *blob.Client {
	t.Helper()
	client, err := azblob.NewClientWithNoCredential(srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("azblob.NewClientWithNoCredential() error = %v", err)
	}
	return client.ServiceClient().NewContainerClient(container).NewBlobClient(blobName)
}

func TestAzurermBlobName(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		workspace string
		want      string
	}{
		{"default workspace unchanged", "terraform.tfstate", "default", "terraform.tfstate"},
		{"empty workspace treated as default", "terraform.tfstate", "", "terraform.tfstate"},
		{"non-default workspace", "terraform.tfstate", "staging", "terraform.tfstateenv:staging"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := azurermBlobName(c.key, c.workspace); got != c.want {
				t.Errorf("azurermBlobName(%q, %q) = %q, want %q", c.key, c.workspace, got, c.want)
			}
		})
	}
}

func TestPeekAzurermLease_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-lease-status", "unlocked")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	bc := testAzureBlobClient(t, srv, "mycontainer", "terraform.tfstate")
	info, supported, err := peekAzurermLease(context.Background(), bc)
	if err != nil {
		t.Fatalf("peekAzurermLease() error = %v", err)
	}
	if !supported {
		t.Fatal("peekAzurermLease() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekAzurermLease_Locked(t *testing.T) {
	metaVal := base64.StdEncoding.EncodeToString([]byte(`{"ID":"abc-123","Who":"runner@github-actions"}`))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-lease-status", "locked")
		w.Header().Set("x-ms-meta-terraformlockid", metaVal)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	bc := testAzureBlobClient(t, srv, "mycontainer", "terraform.tfstate")
	info, supported, err := peekAzurermLease(context.Background(), bc)
	if err != nil {
		t.Fatalf("peekAzurermLease() error = %v", err)
	}
	if !supported {
		t.Fatal("peekAzurermLease() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestPeekAzurermLease_BlobNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-error-code", "BlobNotFound")
		w.WriteHeader(404)
	}))
	defer srv.Close()

	bc := testAzureBlobClient(t, srv, "mycontainer", "terraform.tfstate")
	info, supported, err := peekAzurermLease(context.Background(), bc)
	if err != nil {
		t.Fatalf("peekAzurermLease() error = %v", err)
	}
	if !supported {
		t.Fatal("peekAzurermLease() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestAzurermLockTarget(t *testing.T) {
	cases := []struct {
		name          string
		cfg           map[string]any
		workspace     string
		wantAccount   string
		wantContainer string
		wantBlobName  string
		wantOK        bool
	}{
		{"missing container/key", map[string]any{"storage_account_name": "foo"}, "default", "", "", "", false},
		{
			"default workspace",
			map[string]any{"storage_account_name": "foo", "container_name": "mycontainer", "key": "terraform.tfstate"},
			"default", "foo", "mycontainer", "terraform.tfstate", true,
		},
		{
			"non-default workspace",
			map[string]any{"storage_account_name": "foo", "container_name": "mycontainer", "key": "terraform.tfstate"},
			"staging", "foo", "mycontainer", "terraform.tfstateenv:staging", true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType("azurerm", c.cfg, c.workspace)
			account, container, blobName, ok := azurermLockTarget(cfg)
			if ok != c.wantOK {
				t.Fatalf("azurermLockTarget() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if account != c.wantAccount || container != c.wantContainer || blobName != c.wantBlobName {
				t.Errorf("azurermLockTarget() = (%q, %q, %q), want (%q, %q, %q)",
					account, container, blobName, c.wantAccount, c.wantContainer, c.wantBlobName)
			}
		})
	}
}

func TestAzurermChecker_Peek_MissingConfig(t *testing.T) {
	c := azurermChecker{}
	cfg := testBackendConfig(map[string]any{"storage_account_name": "foo"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing container_name/key)")
	}
}
