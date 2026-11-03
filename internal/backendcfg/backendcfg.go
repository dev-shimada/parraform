// Package backendcfg reads the backend configuration that "terraform init"
// caches locally, so callers can determine which state backend is in use
// without parsing HCL. Backend config blocks cannot use interpolation, so
// this cache is authoritative.
package backendcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config describes the resolved backend for the working directory.
type Config struct {
	Type   string
	Config map[string]any
	// Dir is the working directory the backend config was discovered in,
	// needed to resolve backend config paths that are relative (e.g. the
	// local backend's "path").
	Dir string
	// Workspace is the currently selected terraform workspace, e.g.
	// "default" or "staging". Lock checkers must fold this into whatever
	// state path/key they derive from Config, since non-default
	// workspaces are stored (and locked) at a different location than the
	// backend's bare configured path/key.
	Workspace string
}

type cacheFile struct {
	Backend struct {
		Type   string         `json:"type"`
		Config map[string]any `json:"config"`
	} `json:"backend"`
}

// dataDir mirrors terraform's own resolution of its data directory (where
// the backend cache and the selected workspace live): TF_DATA_DIR if set,
// relative to the working directory, otherwise dir/.terraform. Reading
// .terraform while terraform uses another directory would check the lock of
// a stale or different backend.
func dataDir(dir string) string {
	if d := os.Getenv("TF_DATA_DIR"); d != "" {
		if filepath.IsAbs(d) {
			return d
		}
		return filepath.Join(dir, d)
	}
	return filepath.Join(dir, ".terraform")
}

// Discover reads the backend cache file (terraform.tfstate in the data
// directory, normally dir/.terraform, see dataDir) and returns the backend
// it describes. It returns (nil, nil) when the cache file doesn't exist
// (e.g. before "terraform init" has run, or a purely local backend that
// never wrote one) so callers can treat that as "nothing to check" rather
// than an error.
func Discover(dir string) (*Config, error) {
	path := filepath.Join(dataDir(dir), "terraform.tfstate")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cf.Backend.Type == "" {
		return nil, nil
	}

	return &Config{
		Type:      cf.Backend.Type,
		Config:    cf.Backend.Config,
		Dir:       dir,
		Workspace: currentWorkspace(dir),
	}, nil
}

// currentWorkspace mirrors terraform's own resolution: TF_WORKSPACE
// overrides everything, otherwise the "environment" file in the data
// directory (see dataDir) holds the selected workspace name, and its
// absence means "default" (terraform never writes that file for the default
// workspace).
func currentWorkspace(dir string) string {
	if ws := os.Getenv("TF_WORKSPACE"); ws != "" {
		return ws
	}
	data, err := os.ReadFile(filepath.Join(dataDir(dir), "environment"))
	if err != nil {
		return "default"
	}
	ws := strings.TrimSpace(string(data))
	if ws == "" {
		return "default"
	}
	return ws
}
