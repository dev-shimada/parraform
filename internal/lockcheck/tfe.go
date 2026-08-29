package lockcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tfe "github.com/hashicorp/go-tfe"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	// backend "remote" {} and a cloud {} block both end up talking to the
	// same Terraform Cloud/Enterprise workspace-lock API; only the target
	// workspace resolution differs (see tfeWorkspaceName). Verified from
	// terraform's own source (internal/configs/cloud.go:ToBackendConfig)
	// that a cloud{} block is cached with backend.type == "cloud", a
	// distinct string from the older "remote" backend's type.
	Register("remote", tfeChecker{})
	Register("cloud", tfeChecker{})
}

// tfeChecker peeks a Terraform Cloud/Enterprise workspace's lock via the
// same public Workspaces API terraform's own "remote"/"cloud" backends use
// to acquire it, rather than re-implementing locking logic: TFC/TFE lock
// state is a workspace attribute (Locked bool), not something with a
// separate read-only "peek" primitive to reverse-engineer.
//
// Unlike the other checkers in this package, the "workspaces" nested block
// shape inside the .terraform/terraform.tfstate cache was not verified
// against a real cache file (no TFC/TFE account was available while
// building this), only inferred from terraform's schema source. Extraction
// is deliberately defensive: any shape mismatch falls back to
// supported=false rather than guessing a workspace name.
type tfeChecker struct{}

func (tfeChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	org, _ := cfg.Config["organization"].(string)
	if org == "" {
		return Info{}, false, nil
	}

	hostname, _ := cfg.Config["hostname"].(string)
	if hostname == "" {
		hostname = "app.terraform.io"
	}

	workspace, ok := tfeWorkspaceName(cfg)
	if !ok {
		return Info{}, false, nil
	}

	token := tfeToken(hostname, cfg.Config)
	if token == "" {
		return Info{}, false, nil
	}

	client, err := tfe.NewClient(&tfe.Config{
		Address: "https://" + hostname,
		Token:   token,
	})
	if err != nil {
		return Info{}, false, fmt.Errorf("creating TFC/TFE client: %w", err)
	}

	return peekTFEWorkspaceLock(ctx, client.Workspaces, org, workspace)
}

// tfeWorkspaceName resolves the target TFC/TFE workspace name from the
// backend config, mirroring terraform's own mapping (verified from
// internal/backend/remote/backend.go's StateMgr):
//   - workspaces.name: fixed single workspace, used as-is.
//   - workspaces.prefix (remote backend only): local workspace name is
//     prefixed unless already prefixed; using the default workspace with
//     only "prefix" set is invalid in terraform itself, so that combination
//     reports unsupported here too.
//   - cloud{} block's tags/project-based dynamic workspace matching is not
//     supported: there is no single fixed workspace name to resolve without
//     replicating terraform's matching logic against a live org.
func tfeWorkspaceName(cfg backendcfg.Config) (string, bool) {
	ws, ok := extractNestedBlock(cfg.Config["workspaces"])
	if !ok {
		return "", false
	}

	if name, _ := ws["name"].(string); name != "" {
		return name, true
	}

	if cfg.Type != "remote" {
		// cloud{} has no "prefix"; only tags/project remain, unsupported.
		return "", false
	}

	prefix, _ := ws["prefix"].(string)
	if prefix == "" {
		return "", false
	}
	workspace := cfg.Workspace
	if workspace == "" || workspace == "default" {
		return "", false
	}
	if strings.HasPrefix(workspace, prefix) {
		return workspace, true
	}
	return prefix + workspace, true
}

// extractNestedBlock handles both plausible JSON shapes for a nested HCL
// block in the backend config cache (a single object, or a one-element
// list wrapping it), since the exact shape wasn't empirically confirmed.
func extractNestedBlock(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case []any:
		if len(t) == 1 {
			if m, ok := t[0].(map[string]any); ok {
				return m, true
			}
		}
	}
	return nil, false
}

// tfeToken resolves the API token the same way terraform does: a
// TF_TOKEN_<hostname, dots as underscores> environment variable takes
// precedence, then an explicit "token" backend attribute (remote backend
// only), then the CLI credentials file written by "terraform login".
// Hostnames containing hyphens have an additional terraform-specific env
// var encoding this doesn't replicate; that's a documented gap.
func tfeToken(hostname string, bcfg map[string]any) string {
	envKey := "TF_TOKEN_" + strings.ReplaceAll(hostname, ".", "_")
	if t := os.Getenv(envKey); t != "" {
		return t
	}
	if t, _ := bcfg["token"].(string); t != "" {
		return t
	}
	return credentialsFileToken(hostname)
}

func credentialsFileToken(hostname string) string {
	path, ok := credentialsFilePath()
	if !ok {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cf struct {
		Credentials map[string]struct {
			Token string `json:"token"`
		} `json:"credentials"`
	}
	if json.Unmarshal(data, &cf) != nil {
		return ""
	}
	return cf.Credentials[hostname].Token
}

func credentialsFilePath() (string, bool) {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "terraform.d", "credentials.tfrc.json"), true
		}
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".terraform.d", "credentials.tfrc.json"), true
}

// tfeWorkspaceReader is the subset of the go-tfe Workspaces service used
// here, so tests can point it at a fake server without a real TFC/TFE org.
type tfeWorkspaceReader interface {
	Read(ctx context.Context, organization, workspace string) (*tfe.Workspace, error)
}

func peekTFEWorkspaceLock(ctx context.Context, r tfeWorkspaceReader, org, workspace string) (Info, bool, error) {
	ws, err := r.Read(ctx, org, workspace)
	if err != nil {
		return Info{}, false, err
	}
	return Info{Locked: ws.Locked}, true, nil
}
