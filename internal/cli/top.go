package cli

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
)

// topAliases map a word a person would type to the dimension the API wants.
// The API's names are precise and unmemorable; these are neither, so both
// work: an unknown alias falls through to being used verbatim.
var topAliases = map[string]string{
	"pages":       "event:page",
	"folders":     "event:folder",
	"sources":     "visit:source",
	"referrers":   "visit:referrer",
	"channels":    "visit:channel",
	"countries":   "visit:country",
	"regions":     "visit:region",
	"cities":      "visit:city",
	"browsers":    "visit:browser",
	"os":          "visit:os",
	"devices":     "visit:device",
	"entry":       "visit:entry_page",
	"exit":        "visit:exit_page",
	"hostnames":   "event:hostname",
	"events":      "event:name",
	"goals":       "event:goal",
	"codes":       "event:status_code",
	"utm_source":  "visit:utm_source",
	"utm_medium":  "visit:utm_medium",
	"utm_content": "visit:utm_content",
	"utm_term":    "visit:utm_term",
	"campaigns":   "visit:utm_campaign",
	// Both spellings of the two version dimensions, because neither reads as
	// obviously right and guessing wrong costs a round trip to the help.
	"os_version":       "visit:os_version",
	"os_versions":      "visit:os_version",
	"browser_version":  "visit:browser_version",
	"browser_versions": "visit:browser_version",
}

// defaultMetrics per dimension, chosen as the columns a person actually reads
// first rather than everything the dimension can compute.
var topDefaults = map[string][]string{
	"event:page":        {"visitors", "pageviews"},
	"event:folder":      {"visitors", "pageviews"},
	"event:hostname":    {"visitors", "pageviews"},
	"event:status_code": {"pageviews"},
	"event:name":        {"visitors", "events"},
	"event:goal":        {"visitors", "conversion_rate"},
	"visit:entry_page":  {"visitors", "visits"},
	"visit:exit_page":   {"visitors", "exit_rate"},
}

func topAliasList() []string {
	out := make([]string, 0, len(topAliases))
	for k := range topAliases {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newTopCmd() *cobra.Command {
	var q queryFlags
	cmd := &cobra.Command{
		Use:   "top <what>",
		Short: "The leading values of one dimension",
		Long: "Rank one dimension over a period.\n\n" +
			"<what> is a short name such as pages, sources or countries, or a full\n" +
			"dimension such as visit:utm_campaign. Run `statable top` with no argument\n" +
			"to see the short names.\n\n" +
			"Two dimensions return no comparison at all, whatever --compare says:\n" +
			"codes (event:status_code) and goals (event:goal). The rows come back\n" +
			"without previous figures rather than with zeroes.\n\n" +
			"Examples:\n" +
			"  statable top pages\n" +
			"  statable top sources --range 30d -n 20\n" +
			"  statable top countries --compare previous_period\n" +
			"  statable top pages --filter 'page~/blog' --format csv",
		Args: cobra.MaximumNArgs(1),
		// Completion is the difference between a discoverable dimension list
		// and one that needs the help read first. Both the short names and
		// the full dimensions are offered, because both are accepted.
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			out := append(topAliasList(), query.BreakdownDimensions()...)
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			if len(args) == 0 {
				return clierr.Failf("NO_DIMENSION",
					"say what to rank: %s", strings.Join(topAliasList(), ", ")).
					WithExit(clierr.ExitUsage)
			}

			what := strings.ToLower(strings.TrimSpace(args[0]))
			dim, ok := topAliases[what]
			if !ok {
				// The normalised form is used, not the raw argument, so
				// `top EVENT:PAGE` behaves like `top pages` rather than
				// failing on a dimension that differs only in case.
				dim = what
			}

			metrics := q.metrics
			if len(metrics) == 0 {
				if d, ok := topDefaults[dim]; ok {
					metrics = d
				} else {
					metrics = []string{"visitors"}
				}
			}

			filters, err := q.parseFilters()
			if err != nil {
				return err
			}

			resp, err := rt.runQuery(cmd.Context(), query.Spec{
				Metrics:   metrics,
				DateRange: q.effectiveRange(cmd, rt),
				Dimension: dim,
				Filters:   filters,
				Compare:   q.compare,
				Limit:     q.limit,
				Offset:    q.offset,
				// Paging belongs to a breakdown. Sending it for a time
				// dimension made `top time:day` fail with "limit and offset
				// apply to a breakdown only" instead of naming the real
				// problem, which is that a time bucket is not a ranking.
				HasLimit:  !query.TimeDimensions[dim],
				HasOffset: !query.TimeDimensions[dim] && cmd.Flags().Changed("offset"),
			})
			if err != nil {
				return err
			}

			// Geo dimensions answer with a code plus a readable label. A
			// person wants the label; a script wants the code, because the
			// code is exactly what the matching filter accepts.
			human := rt.Ctx.HumanOutput()
			// The column is keyed on the dimension, not on the word the user
			// typed. Keying on the alias meant `top countries` and
			// `top visit:country` produced different JSON for one request,
			// and no script could write a single extractor.
			t := output.Table{Columns: []string{dimensionKey(dim)}}
			for _, m := range metrics {
				t.Columns = append(t.Columns, m)
				t.Right = append(t.Right, len(t.Columns)-1)
				// Only machine output carries raw numbers; human cells are
				// formatted text and must stay strings.
				if !human {
					t.Numeric = append(t.Numeric, len(t.Columns)-1)
					if q.compare != "" {
						for _, c := range compareColumns(m) {
							t.Columns = append(t.Columns, c)
							t.Numeric = append(t.Numeric, len(t.Columns)-1)
						}
					}
				}
			}
			if !human && query.GeoDimensions[dim] {
				t.Columns = append(t.Columns, "label")
			}

			for _, r := range resp.Results {
				value := dimensionValue(r.Dimensions, dim)
				label := r.Labels[dim]
				shown := value
				if human && label != "" {
					shown = label
				}
				row := []string{shown}
				for _, m := range metrics {
					c, hasCompare := r.Compare[m]
					if human {
						row = append(row, humanWithCompare(formatMetric(m, r.Metrics[m]), c, hasCompare))
						continue
					}
					row = append(row, rawMetric(r.Metrics[m]))
					if q.compare != "" {
						row = append(row, compareCells(c, hasCompare)...)
					}
				}
				if !human && query.GeoDimensions[dim] {
					row = append(row, label)
				}
				t.Rows = append(t.Rows, row)
			}

			if pair := resp.ResolvedRange(); len(pair) > 0 {
				rt.Out.Note("period: %s", query.DescribeRange(pair))
			}
			if len(t.Rows) == 0 {
				// A blank screen with no explanation is the same failure the
				// totals path refuses: exit 0 with no output reads as success
				// with no answer.
				rt.Out.Note("no rows for this period")
			}
			if err := rt.Out.Emit(t); err != nil {
				return err
			}
			if m := resp.Meta; m != nil && m.HasMore {
				// The next offset counts the rows actually received, not the
				// limit that was asked for. When the server returns fewer
				// rows than the limit, advancing by the limit skips the rest.
				rt.Out.Note("showing %d of %d; next page: --offset %d",
					len(resp.Results), m.Total, m.Offset+len(resp.Results))
			}
			return nil
		},
	}
	q.register(cmd, "7d")
	q.registerMetrics(cmd, nil)
	q.registerPaging(cmd)
	return cmd
}
