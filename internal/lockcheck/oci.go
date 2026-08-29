package lockcheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("oci", ociChecker{})
}

const (
	ociDefaultKey                = "terraform.tfstate"
	ociDefaultWorkspaceKeyPrefix = "tf-state-env"
	ociLockFileSuffix            = ".lock"
)

// ociChecker peeks the oci (Oracle Cloud Infrastructure) backend's lock by
// reading its lock object directly. Verified against terraform's source
// (internal/backend/remote-state/oci/backend.go, constants.go, client.go
// via `gh api` raw content, cross-checked against the WebFetch summary):
// Lock() PUTs the LockInfo JSON to "<path(workspace)>.lock" with
// IfNoneMatch:"*" (atomic create-if-absent, the same idea as GCS's
// if-generation-match=0), and Unlock() deletes it -- so the lock object's
// existence alone is authoritative for a read-only peek.
//
// path(name) mirrors S3's shape, not GCS/kubernetes/pg's: the default
// workspace is the bare key, others are
// "<workspace_key_prefix>/<workspace>/<key>" (workspace_key_prefix
// defaults to "tf-state-env", key to "terraform.tfstate") -- confirmed
// from source rather than assumed consistent with the object-storage
// backends done earlier, several of which do NOT special-case default.
type ociChecker struct{}

func (ociChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	bucket, _ := cfg.Config["bucket"].(string)
	namespace, _ := cfg.Config["namespace"].(string)
	if bucket == "" || namespace == "" {
		return Info{}, false, nil
	}
	key, _ := cfg.Config["key"].(string)
	if key == "" {
		key = ociDefaultKey
	}
	prefix, _ := cfg.Config["workspace_key_prefix"].(string)
	if prefix == "" {
		prefix = ociDefaultWorkspaceKeyPrefix
	}

	lockObject := ociLockObjectName(prefix, cfg.Workspace, key)

	client, err := ociClient(cfg.Config)
	if err != nil {
		return Info{}, false, err
	}

	return peekOCILockObject(ctx, client, namespace, bucket, lockObject)
}

// ociLockObjectName reproduces terraform's oci backend Backend.path() +
// getLockFilePath(): default workspace is the bare key, others are
// "<prefix>/<workspace>/<key>", then ".lock" is appended.
func ociLockObjectName(prefix, workspace, key string) string {
	if workspace == "" || workspace == "default" {
		return key + ociLockFileSuffix
	}
	return path.Join(prefix, workspace, key) + ociLockFileSuffix
}

// ociClient builds an Object Storage client. MVP-scoped to the classic API
// key attributes (tenancy_ocid/user_ocid/fingerprint/private_key) when
// present directly in the backend config, falling back to the SDK's
// default config file provider (~/.oci/config) otherwise. Instance
// Principal / Resource Principal / Security Token auth (the "auth"
// attribute's other modes) aren't supported yet.
func ociClient(bcfg map[string]any) (*objectstorage.ObjectStorageClient, error) {
	tenancy, _ := bcfg["tenancy_ocid"].(string)
	user, _ := bcfg["user_ocid"].(string)
	fingerprint, _ := bcfg["fingerprint"].(string)
	privateKey, _ := bcfg["private_key"].(string)

	var provider common.ConfigurationProvider
	if tenancy != "" && user != "" && fingerprint != "" && privateKey != "" {
		region, _ := bcfg["region"].(string)
		passphrase, _ := bcfg["private_key_password"].(string)
		var passphrasePtr *string
		if passphrase != "" {
			passphrasePtr = &passphrase
		}
		provider = common.NewRawConfigurationProvider(tenancy, user, region, fingerprint, privateKey, passphrasePtr)
	} else {
		profile, _ := bcfg["config_file_profile"].(string)
		if profile == "" {
			profile = "DEFAULT"
		}
		provider = common.CustomProfileConfigProvider("", profile)
	}

	client, err := objectstorage.NewObjectStorageClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, err
	}
	return &client, nil
}

// ociObjectGetter is the subset of *objectstorage.ObjectStorageClient used
// here, so tests can point it at a fake server without real OCI
// credentials.
type ociObjectGetter interface {
	GetObject(ctx context.Context, request objectstorage.GetObjectRequest) (objectstorage.GetObjectResponse, error)
}

func peekOCILockObject(ctx context.Context, client ociObjectGetter, namespace, bucket, objectName string) (Info, bool, error) {
	resp, err := client.GetObject(ctx, objectstorage.GetObjectRequest{
		NamespaceName: &namespace,
		BucketName:    &bucket,
		ObjectName:    &objectName,
	})
	if err != nil {
		var svcErr common.ServiceError
		if errors.As(err, &svcErr) && svcErr.GetHTTPStatusCode() == http.StatusNotFound {
			return Info{Locked: false}, true, nil
		}
		return Info{}, false, err
	}
	if resp.Content == nil {
		return Info{Locked: true}, true, nil
	}
	defer resp.Content.Close()

	body, err := io.ReadAll(resp.Content)
	if err != nil {
		return Info{Locked: true}, true, nil
	}

	var li lockInfoJSON
	_ = json.Unmarshal(body, &li)
	return Info{Locked: true, Who: li.Who}, true, nil
}
