package lockcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("gcs", gcsChecker{})
}

// gcsChecker peeks the GCS backend's lock by reading its .tflock object
// directly, mirroring terraform's own lockFile(name) = path.Join(prefix,
// name+".tflock"). Unlike S3/local, GCS does not special-case the default
// workspace: verified against terraform's gcs backend source that its lock
// object is literally "<prefix>/default.tflock", not "<prefix>.tflock".
type gcsChecker struct{}

func (gcsChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	bucket, object, ok := gcsLockTarget(cfg)
	if !ok {
		return Info{}, false, nil
	}

	opts, err := gcsClientOptions(cfg.Config)
	if err != nil {
		return Info{}, false, err
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return Info{}, false, fmt.Errorf("creating GCS client: %w", err)
	}
	defer func() { _ = client.Close() }()

	obj := client.Bucket(bucket).Object(object)
	return peekGCSLockfile(ctx, obj)
}

// gcsLockTarget resolves the bucket and workspace-qualified lock object
// name from raw backend config, so the config-to-identifier wiring itself
// can be exercised directly with a backendcfg.Config, workspace included.
func gcsLockTarget(cfg backendcfg.Config) (bucket, object string, ok bool) {
	bucket, _ = cfg.Config["bucket"].(string)
	if bucket == "" {
		return "", "", false
	}
	prefix, _ := cfg.Config["prefix"].(string)

	workspace := cfg.Workspace
	if workspace == "" {
		workspace = "default"
	}
	return bucket, gcsLockObject(prefix, workspace), true
}

// gcsLockObject reproduces terraform's gcs backend lockFile(name).
func gcsLockObject(prefix, workspace string) string {
	return path.Join(prefix, workspace+".tflock")
}

// gcsClientOptions handles the "credentials" backend attribute, which
// terraform accepts as either a path to a JSON key file or the raw JSON
// key content itself. Other credential attributes (access_token,
// impersonate_service_account) aren't supported yet; when unset, this
// falls back to Application Default Credentials, same as terraform does.
func gcsClientOptions(bcfg map[string]any) ([]option.ClientOption, error) {
	creds, _ := bcfg["credentials"].(string)
	if creds == "" {
		return nil, nil
	}
	if _, err := os.Stat(creds); err == nil {
		return []option.ClientOption{option.WithAuthCredentialsFile(option.ServiceAccount, creds)}, nil
	}
	return []option.ClientOption{option.WithAuthCredentialsJSON(option.ServiceAccount, []byte(creds))}, nil
}

// gcsObjectReader is the subset of *storage.ObjectHandle used here, so
// tests can fake it without a real GCS project.
type gcsObjectReader interface {
	NewReader(ctx context.Context, opts ...storage.ReaderOption) (*storage.Reader, error)
}

func peekGCSLockfile(ctx context.Context, obj gcsObjectReader) (Info, bool, error) {
	r, err := obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return Info{Locked: false}, true, nil
		}
		return Info{}, false, err
	}
	defer func() { _ = r.Close() }()

	body, err := io.ReadAll(r)
	if err != nil {
		// The lock object exists; treat as locked even if we can't read
		// the body describing who holds it.
		return Info{Locked: true}, true, nil
	}

	var li lockInfoJSON
	_ = json.Unmarshal(body, &li)
	return Info{Locked: true, Who: li.Who}, true, nil
}
