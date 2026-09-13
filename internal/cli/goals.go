package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

func newGoalsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "goals",
		Short: "The conversion goals defined on a site",
		Long: "The goals a site has defined, and what each one matches.\n\n" +
			"A goal is a page, a custom event, or a scroll depth. Which of those it\n" +
			"is decides which field carries the target, so the kind is shown beside\n" +
			"it rather than left to be inferred from an empty column.\n\n" +
			"This lists the definitions. For how they performed, ask for the\n" +
			"breakdown instead:\n\n" +
			"  statable top goals --range 30d\n\n" +
			"Examples:\n" +
			"  statable goals\n" +
			"  statable goals --json | jq -r '.[] | select(.kind == \"event\") | .target'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var out api.GoalsResponse
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				resp, rerr := client.Get(cmd.Context(),
					fmt.Sprintf("/sites/%d/goals", site.SiteID), nil)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "goals")
				}
				return api.DecodeInto(resp, &out)
			})
			if err != nil {
				return err
			}
			if len(out.Goals) == 0 {
				rt.Out.Note("this site has no goals; they are created in the dashboard")
			}

			human := rt.Ctx.HumanOutput()
			t := output.Table{
				Columns:   []string{"id", "name", "kind", "target", "operator", "created_at"},
				Right:     []int{0},
				HumanOmit: []int{5},
			}
			if !human {
				t.Numeric = []int{0}
			}
			for _, g := range out.Goals {
				t.Rows = append(t.Rows, []string{
					strconv.FormatInt(g.ID, 10), g.Name, g.Kind(), g.Target(),
					g.Operator, g.CreatedAt,
				})
			}
			return rt.Out.Emit(t)
		},
	}
	cmd.AddCommand(newGoalsCreateCmd(), newGoalsEditCmd(), newGoalsDeleteCmd())
	return cmd
}

// explainWriteDisabled turns one server answer into something actionable.
//
// Both this endpoint and the snippet sit behind the API's write guard, even
// though reading them needs only the read scope. When the deployment has its
// write surface switched off the answer is a 404 with code write_disabled,
// which reads as "no such site" to anyone who does not know that. It is not:
// the site is fine and the key is fine, and no amount of retrying will help.
func explainWriteDisabled(err error, what string) error {
	var e *clierr.Err
	if !errors.As(err, &e) {
		return err
	}
	for _, is := range e.Envelope.Issues {
		if is.Code != "write_disabled" {
			continue
		}
		return &clierr.Err{Envelope: clierr.Envelope{
			Status: clierr.StatusActionRequired,
			Error: "this deployment has the write surface switched off, and " +
				what + " is served from behind it",
			Issues:    []clierr.Issue{{Code: "write_disabled"}},
			NextSteps: []string{"ask whoever runs this deployment to enable the v1 write surface"},
			RequestID: e.Envelope.RequestID,
		}, Cause: err}
	}
	return err
}
