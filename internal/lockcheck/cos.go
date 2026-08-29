package lockcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"

	cos "github.com/tencentyun/cos-go-sdk-v5"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("cos", cosChecker{})
}

// cosChecker peeks the cos (Tencent Cloud Object Storage) backend's lock
// by reading its lock object directly. Verified against terraform's
// source (internal/backend/remote-state/cos/client.go, backend_state.go):
// Lock() checks for existence of "<stateFile>.tflock" and, if absent,
// PUTs the LockInfo JSON to it; Unlock() deletes it. A distributed Tags
// API mutex is used internally to serialize the check-then-create, but is
// irrelevant to a read-only peek -- the lock object's existence alone is
// authoritative. Workspace naming mirrors terraform's stateFile(): the
// default workspace uses "<prefix>/<key>", others "<prefix>/<workspace>/
// <key>", with the lock suffix ".tflock" appended to that path.
type cosChecker struct{}

func (cosChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	bucket, _ := cfg.Config["bucket"].(string)
	region, _ := cfg.Config["region"].(string)
	if bucket == "" || region == "" {
		return Info{}, false, nil
	}
	prefix, _ := cfg.Config["prefix"].(string)
	key, _ := cfg.Config["key"].(string)
	if key == "" {
		key = "terraform.tfstate"
	}

	lockKey := cosLockObjectKey(prefix, cfg.Workspace, key)

	client, err := cosClient(bucket, region, cfg.Config)
	if err != nil {
		return Info{}, false, err
	}

	return peekCOSLockObject(ctx, client.Object, lockKey)
}

// cosLockObjectKey reproduces terraform's cos backend stateFile()+lockFile():
// default workspace is "<prefix>/<key>", others "<prefix>/<workspace>/
// <key>", then ".tflock" is appended.
func cosLockObjectKey(prefix, workspace, key string) string {
	var statePath string
	if workspace == "" || workspace == "default" {
		statePath = path.Join(prefix, key)
	} else {
		statePath = path.Join(prefix, workspace, key)
	}
	return statePath + ".tflock"
}

func cosClient(bucket, region string, bcfg map[string]any) (*cos.Client, error) {
	u, err := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region))
	if err != nil {
		return nil, err
	}

	secretID, _ := bcfg["secret_id"].(string)
	if secretID == "" {
		secretID = os.Getenv("TENCENTCLOUD_SECRET_ID")
	}
	secretKey, _ := bcfg["secret_key"].(string)
	if secretKey == "" {
		secretKey = os.Getenv("TENCENTCLOUD_SECRET_KEY")
	}

	return cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  secretID,
			SecretKey: secretKey,
		},
	}), nil
}

// cosObjectGetter is the subset of *cos.ObjectService used here, so tests
// can point it at a fake server without real Tencent Cloud credentials.
type cosObjectGetter interface {
	Get(ctx context.Context, key string, opt *cos.ObjectGetOptions, id ...string) (*cos.Response, error)
}

func peekCOSLockObject(ctx context.Context, objects cosObjectGetter, key string) (Info, bool, error) {
	resp, err := objects.Get(ctx, key, nil)
	if err != nil {
		if errResp, ok := err.(*cos.ErrorResponse); ok && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound {
			return Info{Locked: false}, true, nil
		}
		return Info{}, false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Info{Locked: true}, true, nil
	}

	var li lockInfoJSON
	_ = json.Unmarshal(body, &li)
	return Info{Locked: true, Who: li.Who}, true, nil
}
