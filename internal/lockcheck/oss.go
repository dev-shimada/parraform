package lockcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"

	"github.com/aliyun/aliyun-tablestore-go-sdk/tablestore"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("oss", ossChecker{})
}

const ossPkName = "LockID"

// ossChecker peeks the oss (Alibaba Cloud OSS) backend's lock, which --
// unlike every OSS-alike (S3/GCS/azurerm/cos/oci) checker in this package
// -- is not an object in the state bucket itself but a row in a separate
// TableStore table. Verified against terraform's source (fetched via `gh
// api` raw content for internal/backend/remote-state/oss/client.go,
// backend.go, backend_state.go): locking is entirely optional and only
// active when "tablestore_table" is configured; the lock row's primary
// key column is "LockID" (constant pkName) with value
// "<bucket>/<stateFile(workspace)>" (lockPath()); stateFile() special-
// cases the default workspace like S3 ("<prefix>/<key>" vs "<prefix>/
// <workspace>/<key>", prefix defaulting to "env:", key to
// "terraform.tfstate").
//
// IMPORTANT: unlike every httptest-verified checker in this package, this
// one has NOT been exercised against a real or faked TableStore server.
// TableStore's wire protocol is protobuf-based (confirmed: the SDK
// imports "github.com/golang/protobuf/proto" throughout), not the plain
// JSON/XML REST APIs that made httptest fakes practical for S3/GCS/
// azurerm/cos/oci -- hand-constructing valid PlainBuffer-encoded protobuf
// responses was out of scope here. Treat this checker at the same
// confidence tier as pg: source-verified, integration-unverified.
type ossChecker struct{}

func (ossChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	table, _ := cfg.Config["tablestore_table"].(string)
	if table == "" {
		// Locking via TableStore is opt-in in this backend; if unset,
		// terraform itself never locks.
		return Info{}, false, nil
	}
	bucket, _ := cfg.Config["bucket"].(string)
	if bucket == "" {
		return Info{}, false, nil
	}
	instanceName, _ := cfg.Config["tablestore_instance_name"].(string)
	endpoint, _ := cfg.Config["tablestore_endpoint"].(string)
	if instanceName == "" || endpoint == "" {
		return Info{}, false, nil
	}

	prefix, _ := cfg.Config["prefix"].(string)
	if prefix == "" {
		prefix = "env:"
	}
	key, _ := cfg.Config["key"].(string)
	if key == "" {
		key = "terraform.tfstate"
	}

	lockPath := bucket + "/" + ossStateFile(prefix, cfg.Workspace, key)

	accessKey, _ := cfg.Config["access_key"].(string)
	if accessKey == "" {
		accessKey = os.Getenv("ALICLOUD_ACCESS_KEY")
	}
	secretKey, _ := cfg.Config["secret_key"].(string)
	if secretKey == "" {
		secretKey = os.Getenv("ALICLOUD_SECRET_KEY")
	}

	client := tablestore.NewClient(endpoint, instanceName, accessKey, secretKey)
	return peekOSSLockRow(client, table, lockPath)
}

// ossStateFile reproduces terraform's oss backend Backend.stateFile():
// the default workspace is "<prefix>/<key>"; others insert the workspace
// as a middle path segment, "<prefix>/<workspace>/<key>".
func ossStateFile(prefix, workspace, key string) string {
	if workspace == "" || workspace == "default" {
		return path.Join(prefix, key)
	}
	return path.Join(prefix, workspace, key)
}

// ossTableStoreGetRow is the subset of *tablestore.TableStoreClient used
// here.
type ossTableStoreGetRow interface {
	GetRow(request *tablestore.GetRowRequest) (*tablestore.GetRowResponse, error)
}

func peekOSSLockRow(client ossTableStoreGetRow, table, lockPath string) (Info, bool, error) {
	resp, err := client.GetRow(&tablestore.GetRowRequest{
		SingleRowQueryCriteria: &tablestore.SingleRowQueryCriteria{
			TableName: table,
			PrimaryKey: &tablestore.PrimaryKey{
				PrimaryKeys: []*tablestore.PrimaryKeyColumn{
					{ColumnName: ossPkName, Value: lockPath},
				},
			},
			MaxVersion: 1,
		},
	})
	if err != nil {
		return Info{}, false, fmt.Errorf("tablestore GetRow: %w", err)
	}
	if len(resp.Columns) == 0 {
		return Info{Locked: false}, true, nil
	}

	who := ""
	for _, col := range resp.Columns {
		if col.ColumnName != "Info" {
			continue
		}
		if s, ok := col.Value.(string); ok {
			var li lockInfoJSON
			if json.Unmarshal([]byte(s), &li) == nil {
				who = li.Who
			}
		}
	}
	return Info{Locked: true, Who: who}, true, nil
}
