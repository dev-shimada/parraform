package lockcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// testKubeconfigPath writes a kubeconfig pointing at srv (with TLS
// verification disabled, since httptest.Server serves plain HTTP/HTTPS
// without a trusted cert) and returns its path.
func testKubeconfigPath(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	kubeconfig := fmt.Sprintf(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user: {}
`, srv.URL)
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKubernetesLeaseName(t *testing.T) {
	cases := []struct {
		name      string
		workspace string
		suffix    string
		want      string
	}{
		{"default workspace, no special case", "default", "state", "lock-tfstate-default-state"},
		{"non-default workspace", "staging", "state", "lock-tfstate-staging-state"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kubernetesLeaseName(c.workspace, c.suffix); got != c.want {
				t.Errorf("kubernetesLeaseName(%q, %q) = %q, want %q", c.workspace, c.suffix, got, c.want)
			}
		})
	}
}

func TestPeekKubernetesLease_NoLeaseYet(t *testing.T) {
	client := fake.NewSimpleClientset()
	info, supported, err := peekKubernetesLease(context.Background(), client.CoordinationV1().Leases("default"), "lock-tfstate-default-state")
	if err != nil {
		t.Fatalf("peekKubernetesLease() error = %v", err)
	}
	if !supported {
		t.Fatal("peekKubernetesLease() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekKubernetesLease_ExistsButNotLocked(t *testing.T) {
	// Mirrors terraform's Unlock(): the lease persists but HolderIdentity
	// is cleared back to nil rather than the lease being deleted.
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "lock-tfstate-default-state", Namespace: "default"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: nil},
	}
	client := fake.NewSimpleClientset(lease)

	info, supported, err := peekKubernetesLease(context.Background(), client.CoordinationV1().Leases("default"), "lock-tfstate-default-state")
	if err != nil {
		t.Fatalf("peekKubernetesLease() error = %v", err)
	}
	if !supported {
		t.Fatal("peekKubernetesLease() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekKubernetesLease_Locked(t *testing.T) {
	holder := "abc-123"
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lock-tfstate-default-state",
			Namespace: "default",
			Annotations: map[string]string{
				kubernetesLockInfoAnnotation: `{"ID":"abc-123","Who":"runner@github-actions"}`,
			},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}
	client := fake.NewSimpleClientset(lease)

	info, supported, err := peekKubernetesLease(context.Background(), client.CoordinationV1().Leases("default"), "lock-tfstate-default-state")
	if err != nil {
		t.Fatalf("peekKubernetesLease() error = %v", err)
	}
	if !supported {
		t.Fatal("peekKubernetesLease() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestKubernetesLockTarget(t *testing.T) {
	cases := []struct {
		name          string
		cfg           map[string]any
		workspace     string
		wantNamespace string
		wantLeaseName string
		wantOK        bool
	}{
		{"missing secret_suffix", map[string]any{"namespace": "default"}, "default", "", "", false},
		{"namespace defaults", map[string]any{"secret_suffix": "state"}, "default", "default", "lock-tfstate-default-state", true},
		{"custom namespace, non-default workspace", map[string]any{"namespace": "tf", "secret_suffix": "state"}, "staging", "tf", "lock-tfstate-staging-state", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType("kubernetes", c.cfg, c.workspace)
			namespace, leaseName, ok := kubernetesLockTarget(cfg)
			if ok != c.wantOK {
				t.Fatalf("kubernetesLockTarget() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if namespace != c.wantNamespace || leaseName != c.wantLeaseName {
				t.Errorf("kubernetesLockTarget() = (%q, %q), want (%q, %q)", namespace, leaseName, c.wantNamespace, c.wantLeaseName)
			}
		})
	}
}

// TestKubernetesChecker_Peek_EndToEnd is the one checker in this package
// exercised through the full production chain -- config extraction,
// kubeconfig-based client construction, lease-name resolution, and lease
// interpretation -- against a real HTTP server, rather than at the
// config-to-identifier and identifier-to-Info layers separately. It's the
// template for what "guaranteed correct" looks like; the other checkers
// stop at the two disconnected layers documented in DESIGN.md.
func TestKubernetesChecker_Peek_EndToEnd(t *testing.T) {
	holder := "abc-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/coordination.k8s.io/v1/namespaces/default/leases/lock-tfstate-staging-state" {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		lease := coordinationv1.Lease{
			TypeMeta:   metav1.TypeMeta{Kind: "Lease", APIVersion: "coordination.k8s.io/v1"},
			ObjectMeta: metav1.ObjectMeta{Name: "lock-tfstate-staging-state", Namespace: "default"},
			Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lease)
	}))
	defer srv.Close()

	c := kubernetesChecker{}
	cfg := testBackendConfigWithType("kubernetes", map[string]any{
		"secret_suffix": "state",
		"config_path":   testKubeconfigPath(t, srv),
	}, "staging")

	info, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if !supported {
		t.Fatal("Peek() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
}

func TestKubernetesChecker_Peek_MissingSecretSuffix(t *testing.T) {
	c := kubernetesChecker{}
	cfg := testBackendConfig(map[string]any{"namespace": "default"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing secret_suffix)")
	}
}
