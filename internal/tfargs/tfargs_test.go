package tfargs

import (
	"reflect"
	"testing"
)

func TestSubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"plain plan", []string{"plan"}, "plan"},
		{"with flags after", []string{"plan", "-var-file=x.tfvars"}, "plan"},
		{"chdir before subcommand", []string{"-chdir=envs/prod", "apply"}, "apply"},
		{"help flag only", []string{"-help"}, ""},
		{"version flag only", []string{"-version"}, ""},
		{"empty", []string{}, ""},
		{"state subcommand", []string{"state", "list"}, "state"},
		{"multiple global flags", []string{"-chdir=x", "-help", "plan"}, "plan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Subcommand(c.args); got != c.want {
				t.Errorf("Subcommand(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestChdir(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no chdir", []string{"plan"}, ""},
		{"chdir before subcommand", []string{"-chdir=envs/prod", "plan"}, "envs/prod"},
		{"chdir among other leading flags", []string{"-help", "-chdir=envs/prod", "plan"}, "envs/prod"},
		{"chdir not leading is ignored", []string{"plan", "-chdir=envs/prod"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Chdir(c.args); got != c.want {
				t.Errorf("Chdir(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestPlanEnv(t *testing.T) {
	t.Run("adds new var", func(t *testing.T) {
		in := []string{"PATH=/bin", "HOME=/home/x"}
		got := PlanEnv(in)
		want := []string{"PATH=/bin", "HOME=/home/x", "TF_CLI_ARGS_plan=-lock=false"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PlanEnv(%v) = %v, want %v", in, got, want)
		}
	})

	t.Run("appends to existing var", func(t *testing.T) {
		in := []string{"TF_CLI_ARGS_plan=-var-file=x.tfvars"}
		got := PlanEnv(in)
		want := []string{"TF_CLI_ARGS_plan=-var-file=x.tfvars -lock=false"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PlanEnv(%v) = %v, want %v", in, got, want)
		}
	})

	t.Run("does not mutate input slice", func(t *testing.T) {
		in := []string{"TF_CLI_ARGS_plan=-x"}
		_ = PlanEnv(in)
		if in[0] != "TF_CLI_ARGS_plan=-x" {
			t.Errorf("input slice was mutated: %v", in)
		}
	})
}
