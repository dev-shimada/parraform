// Package cli builds the parraform command tree. The tree exists only to
// give cobra's generated shell completion something real to complete against
// ("parraform pl<TAB>" -> "plan"); every registered command has flag parsing
// disabled and forwards the untouched argv to the same passthrough handler,
// so the tree never changes actual behavior.
package cli

import "github.com/spf13/cobra"

// terraformCommand names and their one-line descriptions, taken verbatim
// from `terraform -help` (Terraform 1.15). New terraform subcommands won't
// autocomplete until added here, but they still run correctly: unmatched
// args fall through to the root command's own passthrough handler.
var terraformCommands = []struct {
	Use   string
	Short string
}{
	{"init", "Prepare your working directory for other commands"},
	{"validate", "Check whether the configuration is valid"},
	{"plan", "Show changes required by the current configuration"},
	{"apply", "Create or update infrastructure"},
	{"destroy", "Destroy previously-created infrastructure"},
	{"console", "Try Terraform expressions at an interactive command prompt"},
	{"fmt", "Reformat your configuration in the standard style"},
	{"force-unlock", "Release a stuck lock on the current workspace"},
	{"get", "Install or upgrade remote Terraform modules"},
	{"graph", "Generate a Graphviz graph of the steps in an operation"},
	// terraform has no "help" subcommand ("terraform help" prints an error
	// saying so). Registering it here as an ordinary passthrough entry
	// preempts cobra's own auto-added "help" command, which would otherwise
	// intercept it and print cobra's help instead of terraform's real
	// (error) output.
	{"help", "(passes through to terraform, which has no \"help\" command)"},
	{"import", "Associate existing infrastructure with a Terraform resource"},
	{"login", "Obtain and save credentials for a remote host"},
	{"logout", "Remove locally-stored credentials for a remote host"},
	{"metadata", "Metadata related commands"},
	{"modules", "Show all declared modules in a working directory"},
	{"output", "Show output values from your root module"},
	{"providers", "Show the providers required for this configuration"},
	{"query", "Search and list remote infrastructure with Terraform"},
	{"refresh", "Update the state to match remote systems"},
	{"show", "Show the current state or a saved plan"},
	{"stacks", "Manage HCP Terraform stack operations"},
	{"state", "Advanced state management"},
	{"taint", "Mark a resource instance as not fully functional"},
	{"test", "Execute integration tests for Terraform modules"},
	{"untaint", "Remove the 'tainted' state from a resource instance"},
	{"version", "Show the current Terraform version"},
	{"workspace", "Workspace management"},
}

// PassthroughFunc runs the given raw terraform argv (os.Args[1:], untouched)
// and does not return on success.
type PassthroughFunc func(argv []string) error

// NewRootCommand builds the parraform root command. run is called with the
// verbatim argv regardless of which registered subcommand cobra matched, so
// cobra's command tree can never diverge from actual passthrough behavior.
func NewRootCommand(rawArgs func() []string, run PassthroughFunc) *cobra.Command {
	passthrough := func(cmd *cobra.Command, args []string) error {
		return run(rawArgs())
	}

	root := &cobra.Command{
		Use:                   "parraform [global options] <subcommand> [args]",
		Short:                 "Transparent terraform wrapper with lock-free parallel plan",
		DisableFlagParsing:    true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE:                  passthrough,
	}

	for _, c := range terraformCommands {
		root.AddCommand(&cobra.Command{
			Use:                c.Use,
			Short:              c.Short,
			DisableFlagParsing: true,
			SilenceUsage:       true,
			SilenceErrors:      true,
			RunE:               passthrough,
		})
	}

	return root
}
