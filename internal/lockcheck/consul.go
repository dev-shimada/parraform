package lockcheck

import (
	"context"
	"encoding/json"
	"strings"

	consulapi "github.com/hashicorp/consul/api"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("consul", consulChecker{})
}

const consulLockInfoSuffix = "/.lockinfo"

// consulChecker peeks the consul backend's lock with a plain KV GET,
// mirroring terraform's own getLockInfo(). Verified against terraform's
// source (internal/backend/remote-state/consul/client.go): lock info is
// written to "<path>/.lockinfo" on Lock() and explicitly deleted on
// Unlock(), so the key's mere presence is authoritative for "currently
// locked" — no need to inspect Consul sessions directly.
type consulChecker struct{}

func (consulChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	lockPath, ok := consulLockTarget(cfg)
	if !ok {
		return Info{}, false, nil
	}

	client, err := consulClient(cfg.Config)
	if err != nil {
		return Info{}, false, err
	}

	return peekConsulLockInfo(ctx, client.KV(), lockPath)
}

// consulLockTarget resolves the workspace-qualified state path from raw
// backend config, so the config-to-identifier wiring itself can be
// exercised directly with a backendcfg.Config, workspace included.
func consulLockTarget(cfg backendcfg.Config) (lockPath string, ok bool) {
	path, _ := cfg.Config["path"].(string)
	if path == "" {
		return "", false
	}
	if lock, ok := cfg.Config["lock"].(bool); ok && !lock {
		// terraform itself never locks this backend.
		return "", false
	}
	return consulStatePath(path, cfg.Workspace), true
}

// consulStatePath reproduces terraform's consul backend Backend.statePath():
// the default workspace uses the configured path unchanged, any other
// workspace appends the literal "-env:<workspace>" (no separator).
func consulStatePath(path, workspace string) string {
	if workspace == "" || workspace == "default" {
		return path
	}
	return path + "-env:" + workspace
}

func consulClient(bcfg map[string]any) (*consulapi.Client, error) {
	config := consulapi.DefaultConfig()
	if address, _ := bcfg["address"].(string); address != "" {
		config.Address = address
	}
	if scheme, _ := bcfg["scheme"].(string); scheme != "" {
		config.Scheme = scheme
	}
	if datacenter, _ := bcfg["datacenter"].(string); datacenter != "" {
		config.Datacenter = datacenter
	}
	if token, _ := bcfg["access_token"].(string); token != "" {
		config.Token = token
	}
	return consulapi.NewClient(config)
}

// consulKVGetter is the subset of *consulapi.KV used here, so tests can
// point it at a real (docker) or fake consul agent.
type consulKVGetter interface {
	Get(key string, q *consulapi.QueryOptions) (*consulapi.KVPair, *consulapi.QueryMeta, error)
}

func peekConsulLockInfo(ctx context.Context, kv consulKVGetter, lockPath string) (Info, bool, error) {
	key := strings.TrimRight(lockPath, "/") + consulLockInfoSuffix
	pair, _, err := kv.Get(key, (&consulapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return Info{}, false, err
	}
	if pair == nil {
		return Info{Locked: false}, true, nil
	}

	var li lockInfoJSON
	_ = json.Unmarshal(pair.Value, &li)
	return Info{Locked: true, Who: li.Who}, true, nil
}
