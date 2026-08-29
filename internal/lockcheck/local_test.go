//go:build !windows

package lockcheck

import "testing"

func TestWorkspaceStatePath(t *testing.T) {
	cases := []struct {
		name      string
		basePath  string
		workspace string
		want      string
	}{
		{"default workspace unchanged", "terraform.tfstate", "default", "terraform.tfstate"},
		{"empty workspace treated as default", "terraform.tfstate", "", "terraform.tfstate"},
		{"non-default workspace", "terraform.tfstate", "staging", "terraform.tfstate.d/staging/terraform.tfstate"},
		{"non-default workspace with directory in path", "state/terraform.tfstate", "staging", "state/terraform.tfstate.d/staging/terraform.tfstate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workspaceStatePath(c.basePath, c.workspace); got != c.want {
				t.Errorf("workspaceStatePath(%q, %q) = %q, want %q", c.basePath, c.workspace, got, c.want)
			}
		})
	}
}
