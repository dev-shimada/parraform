package lockcheck

import (
	"context"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
