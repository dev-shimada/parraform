package lockcheck

import (
	"context"
	"encoding/json"
	"strings"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("kubernetes", kubernetesChecker{})
}

const kubernetesLockInfoAnnotation = "app.terraform.io/lock-info"

// kubernetesChecker peeks the kubernetes backend's lock by reading its
// coordination.k8s.io/v1 Lease object directly. Verified against
// terraform's source (internal/backend/remote-state/kubernetes/client.go,
// backend_state.go):
//   - Lease name is "lock-tfstate-<workspace>-<secret_suffix>", built via
//     createSecretName()/createLeaseName(); the workspace name has no
//     default-workspace special case (used literally, like GCS).
//   - Lock() sets Spec.HolderIdentity and a lock-info annotation on the
//     lease; Unlock() clears HolderIdentity back to nil rather than
//     deleting the lease. So "locked" is HolderIdentity != nil, not mere
//     lease existence.
type kubernetesChecker struct{}

func (kubernetesChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	namespace, _ := cfg.Config["namespace"].(string)
	if namespace == "" {
		namespace = "default"
	}
	suffix, _ := cfg.Config["secret_suffix"].(string)
	if suffix == "" {
		return Info{}, false, nil
	}

	workspace := cfg.Workspace
	if workspace == "" {
		workspace = "default"
	}
	leaseName := kubernetesLeaseName(workspace, suffix)

	client, err := kubernetesClientset(cfg.Config)
	if err != nil {
		return Info{}, false, err
	}

	return peekKubernetesLease(ctx, client.CoordinationV1().Leases(namespace), leaseName)
}

// kubernetesLeaseName reproduces terraform's kubernetes backend naming:
// "lock-" + strings.Join([]string{"tfstate", workspace, secretSuffix}, "-").
func kubernetesLeaseName(workspace, secretSuffix string) string {
	return "lock-" + strings.Join([]string{"tfstate", workspace, secretSuffix}, "-")
}

func kubernetesClientset(bcfg map[string]any) (*kubernetes.Clientset, error) {
	var restCfg *rest.Config
	var err error

	if inCluster, _ := bcfg["in_cluster_config"].(bool); inCluster {
		restCfg, err = rest.InClusterConfig()
	} else {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		if path, _ := bcfg["config_path"].(string); path != "" {
			loadingRules.ExplicitPath = path
		}
		overrides := &clientcmd.ConfigOverrides{}
		if context, _ := bcfg["config_context"].(string); context != "" {
			overrides.CurrentContext = context
		}
		restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	}
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(restCfg)
}

// kubernetesLeaseGetter is the subset of the LeaseInterface used here, so
// tests can use client-go's fake clientset instead of a real cluster.
type kubernetesLeaseGetter interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*coordinationv1.Lease, error)
}

func peekKubernetesLease(ctx context.Context, leases kubernetesLeaseGetter, name string) (Info, bool, error) {
	lease, err := leases.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Info{Locked: false}, true, nil
		}
		return Info{}, false, err
	}

	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return Info{Locked: false}, true, nil
	}

	who := ""
	if raw, ok := lease.Annotations[kubernetesLockInfoAnnotation]; ok {
		var li lockInfoJSON
		if json.Unmarshal([]byte(raw), &li) == nil {
			who = li.Who
		}
	}
	return Info{Locked: true, Who: who}, true, nil
}
