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

func TestLockCheckMode(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantMode    string
		wantPresent bool
		wantRest    []string
	}{
		{"not given", []string{"plan"}, "", false, []string{"plan"}},
		{"equals form", []string{"plan", "-lock-check=strict"}, "strict", true, []string{"plan"}},
		{"double-dash equals form", []string{"plan", "--lock-check=strict"}, "strict", true, []string{"plan"}},
		{"space form", []string{"plan", "-lock-check", "strict"}, "strict", true, []string{"plan"}},
		{"double-dash space form", []string{"plan", "--lock-check", "strict"}, "strict", true, []string{"plan"}},
		{"among other flags", []string{"plan", "-var-file=x.tfvars", "-lock-check=strict", "-lock=false"}, "strict", true, []string{"plan", "-var-file=x.tfvars", "-lock=false"}},
		{"before subcommand", []string{"-lock-check=strict", "plan"}, "strict", true, []string{"plan"}},
		{"empty value", []string{"plan", "-lock-check="}, "", true, []string{"plan"}},
		{"bare flag at end of args", []string{"plan", "-lock-check"}, "", true, []string{"plan"}},
		{"last occurrence wins", []string{"plan", "-lock-check=warn", "-lock-check=strict"}, "strict", true, []string{"plan"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, present, rest := LockCheckMode(c.args)
			if mode != c.wantMode || present != c.wantPresent {
				t.Errorf("LockCheckMode(%v) = (%q, %v), want (%q, %v)", c.args, mode, present, c.wantMode, c.wantPresent)
			}
			if !reflect.DeepEqual(rest, c.wantRest) {
				t.Errorf("LockCheckMode(%v) rest = %v, want %v", c.args, rest, c.wantRest)
			}
		})
	}

	t.Run("does not mutate input slice", func(t *testing.T) {
		in := []string{"plan", "-lock-check=strict"}
		_, _, _ = LockCheckMode(in)
		want := []string{"plan", "-lock-check=strict"}
		if !reflect.DeepEqual(in, want) {
			t.Errorf("input slice was mutated: %v", in)
		}
	})
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

	t.Run("existing value with no lock-check flag is carried through byte-for-byte, quoting included", func(t *testing.T) {
		in := []string{`TF_CLI_ARGS_plan=-var-file="a b.tfvars"`}
		got := PlanEnv(in)
		want := []string{`TF_CLI_ARGS_plan=-var-file="a b.tfvars" -lock=false`}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PlanEnv(%v) = %v, want %v", in, got, want)
		}
	})

	t.Run("strips a lock-check flag out of the existing value", func(t *testing.T) {
		in := []string{"TF_CLI_ARGS_plan=-var-file=x.tfvars -lock-check=strict"}
		got := PlanEnv(in)
		want := []string{"TF_CLI_ARGS_plan=-var-file=x.tfvars -lock=false"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PlanEnv(%v) = %v, want %v", in, got, want)
		}
	})
}

func TestLockCheckModeFromPlanEnv(t *testing.T) {
	cases := []struct {
		name        string
		env         []string
		wantMode    string
		wantPresent bool
	}{
		{"no TF_CLI_ARGS_plan at all", []string{"PATH=/bin"}, "", false},
		{"TF_CLI_ARGS_plan without the flag", []string{"TF_CLI_ARGS_plan=-var-file=x.tfvars"}, "", false},
		{"equals form", []string{"TF_CLI_ARGS_plan=-lock-check=strict"}, "strict", true},
		{"space form among other flags", []string{"TF_CLI_ARGS_plan=-var-file=x.tfvars -lock-check strict"}, "strict", true},
		{"a quoted argument elsewhere in the value doesn't confuse detection", []string{`TF_CLI_ARGS_plan=-var-file="a b.tfvars" -lock-check=strict`}, "strict", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, present := LockCheckModeFromPlanEnv(c.env)
			if mode != c.wantMode || present != c.wantPresent {
				t.Errorf("LockCheckModeFromPlanEnv(%v) = (%q, %v), want (%q, %v)", c.env, mode, present, c.wantMode, c.wantPresent)
			}
		})
	}
}

func TestLockOverride(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantExpl  bool
		wantValue bool
	}{
		{"not given", []string{"plan"}, false, false},
		{"explicit true via equals", []string{"plan", "-lock=true"}, true, true},
		{"explicit false via equals", []string{"plan", "-lock=false"}, true, false},
		{"double-dash equals", []string{"plan", "--lock=true"}, true, true},
		{"bare flag means true", []string{"plan", "-lock"}, true, true},
		{"bare double-dash flag means true", []string{"plan", "--lock"}, true, true},
		{"lock-timeout must not be mistaken for lock", []string{"plan", "-lock-timeout=0s"}, false, false},
		{"lock-timeout alongside a real explicit -lock", []string{"plan", "-lock-timeout=0s", "-lock=true"}, true, true},
		{"unparseable value is ignored", []string{"plan", "-lock=maybe"}, false, false},
		{"last occurrence wins", []string{"plan", "-lock=true", "-lock=false"}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			explicit, value := LockOverride(c.args)
			if explicit != c.wantExpl || (explicit && value != c.wantValue) {
				t.Errorf("LockOverride(%v) = (%v, %v), want (%v, %v)", c.args, explicit, value, c.wantExpl, c.wantValue)
			}
		})
	}
}
