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
)

// Config describes the resolved backend for the working directory.
type Config struct {
	Type   string
	Config map[string]any
	// Dir is the working directory the backend config was discovered in,
	// needed to resolve backend config paths that are relative (e.g. the
	// local backend's "path").
	Dir string
}

type cacheFile struct {
	Backend struct {
		Type   string         `json:"type"`
		Config map[string]any `json:"config"`
	} `json:"backend"`
}

// Discover reads dir/.terraform/terraform.tfstate and returns the backend
// it describes. It returns (nil, nil) when the cache file doesn't exist
// (e.g. before "terraform init" has run, or a purely local backend that
// never wrote one) so callers can treat that as "nothing to check" rather
// than an error.
func Discover(dir string) (*Config, error) {
	path := filepath.Join(dir, ".terraform", "terraform.tfstate")
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

	return &Config{Type: cf.Backend.Type, Config: cf.Backend.Config, Dir: dir}, nil
}
