package cli

import (
	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
)

// helpOrUsage backs a command that has subcommands but no action of its own.
//
// For a person it prints help. For a machine it is a usage error: help is
// prose, and prose on stdout breaks the caller that asked for JSON or CSV.
// Returning the error also gives a non-zero exit, so a script does not read
// "no arguments" as success.
func helpOrUsage(cmd *cobra.Command) error {
	rt := From(cmd.Context())
	if rt != nil && rt.Ctx.Format.Machine() {
		// The root command's own name is already "statable", so appending it
		// produced the next step "statable statable --help", which is not a
		// command. Only a subcommand needs its name spelled out.
		steps := []string{"statable --help"}
		if cmd.HasParent() {
			steps = append(steps, "statable "+cmd.Name()+" --help")
		}
		return clierr.Fail("NO_COMMAND",
			"no command was given, and there is nothing machine-readable to return",
			steps...).WithExit(clierr.ExitUsage)
	}
	return cmd.Help()
}
