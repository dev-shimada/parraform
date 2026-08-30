package lockcheck

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/lease"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("azurerm", azurermChecker{})
}

const azurermLockMetaKey = "terraformlockid"

// azurermChecker peeks the azurerm backend's lock. Unlike S3/GCS, this
// backend leases the state blob itself directly (no separate lock object)
// and stores lock info (the same Who/ID/... JSON shape as other backends)
// base64-encoded in a blob metadata key, not the blob body. Verified
// against terraform's own azurerm backend source (client.go: Lock() checks
// GetProperties().LeaseStatus, lock info lives in metadata key
// "terraformlockid") and against the real SDK's wire format via a local
// httptest probe (HEAD request, "x-ms-lease-status" header, "x-ms-meta-*"
// headers surfaced as a case-folded Metadata map).
type azurermChecker struct{}

func (azurermChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	account, container, blobName, ok := azurermLockTarget(cfg)
	if !ok {
		return Info{}, false, nil
	}

	client, err := azurermClient(account, cfg.Config)
	if err != nil {
		return Info{}, false, fmt.Errorf("creating Azure blob client: %w", err)
	}

	bc := client.ServiceClient().NewContainerClient(container).NewBlobClient(blobName)
	return peekAzurermLease(ctx, bc)
}

// azurermLockTarget resolves the storage account, container, and
// workspace-qualified blob name from raw backend config, so the
// config-to-identifier wiring itself can be exercised directly with a
// backendcfg.Config, workspace included.
func azurermLockTarget(cfg backendcfg.Config) (account, container, blobName string, ok bool) {
	account, _ = cfg.Config["storage_account_name"].(string)
	container, _ = cfg.Config["container_name"].(string)
	key, _ := cfg.Config["key"].(string)
	if account == "" || container == "" || key == "" {
		return "", "", "", false
	}
	return account, container, azurermBlobName(key, cfg.Workspace), true
}

// azurermBlobName reproduces terraform's azurerm backend Backend.path():
// the default workspace uses the configured key unchanged, any other
// workspace appends the literal string "env:<workspace>" with no
// separator (verified against terraform's source, not our own guess).
func azurermBlobName(key, workspace string) string {
	if workspace == "" || workspace == "default" {
		return key
	}
	return key + "env:" + workspace
}

func azurermClient(account string, bcfg map[string]any) (*azblob.Client, error) {
	endpoint := fmt.Sprintf("https://%s.blob.core.windows.net/", account)

	if accessKey, _ := bcfg["access_key"].(string); accessKey != "" {
		cred, err := azblob.NewSharedKeyCredential(account, accessKey)
		if err != nil {
			return nil, err
		}
		return azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}
	return azblob.NewClient(endpoint, cred, nil)
}

// azurermBlobProperties is the subset of *blob.Client used here, so tests
// can fake it without a real Azure storage account.
type azurermBlobProperties interface {
	GetProperties(ctx context.Context, o *blob.GetPropertiesOptions) (blob.GetPropertiesResponse, error)
}

func peekAzurermLease(ctx context.Context, bc azurermBlobProperties) (Info, bool, error) {
	props, err := bc.GetProperties(ctx, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return Info{Locked: false}, true, nil
		}
		return Info{}, false, err
	}

	locked := props.LeaseStatus != nil && *props.LeaseStatus == lease.StatusTypeLocked
	if !locked {
		return Info{Locked: false}, true, nil
	}

	for k, v := range props.Metadata {
		if v == nil || !strings.EqualFold(k, azurermLockMetaKey) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(*v)
		if err != nil {
			break
		}
		var li lockInfoJSON
		if json.Unmarshal(raw, &li) == nil {
			return Info{Locked: true, Who: li.Who}, true, nil
		}
		break
	}
	return Info{Locked: true}, true, nil
}
