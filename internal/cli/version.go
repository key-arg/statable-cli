package cli

import (
	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/output"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Long: "Print the version, and enough about the build to act on a bug report.\n\n" +
			"A release carries its tag. A binary from `go install` carries the module\n" +
			"version Go recorded, and one built from a clone carries its commit — so\n" +
			"there is always something to name, rather than the word \"dev\" forever.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			b := BuildInfo()
			rec := output.Record{
				{Name: "version", Value: b.Version, Human: b.String()},
				{Name: "go", Value: b.Go, OmitHuman: true},
				{Name: "os", Value: b.OS, OmitHuman: true},
				{Name: "arch", Value: b.Arch, OmitHuman: true},
			}
			// Absent is not empty: a build with no commit recorded should not
			// claim to have one that is the empty string.
			commit := any(nil)
			if b.Commit != "" {
				commit = b.Commit
			}
			date := any(nil)
			if b.Date != "" {
				date = b.Date
			}
			rec = append(rec,
				output.Field{Name: "commit", Value: commit, OmitHuman: true},
				output.Field{Name: "date", Value: date, OmitHuman: true},
				output.Field{Name: "modified", Value: b.Modified, OmitHuman: true},
			)
			return rt.Out.EmitRecord(rec)
		},
	}
}
