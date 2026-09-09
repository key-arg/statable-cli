package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
)

func newStatsCmd() *cobra.Command {
	var q queryFlags
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Headline numbers for a period",
		Long: "The totals for one period, in one line each.\n\n" +
			"Four metrics by default: visitors, pageviews, bounce rate and average\n" +
			"visit duration. --metric replaces that list rather than adding to it.\n\n" +
			"--compare adds the previous period's figure and the percent change. The\n" +
			"comparison window has to cover the same number of days as the range, so\n" +
			"previous_period is the form to reach for unless you mean a specific one.\n\n" +
			"Examples:\n" +
			"  statable stats\n" +
			"  statable stats --range 30d --compare previous_period\n" +
			"  statable stats -m visitors,visits --filter country=DE\n" +
			"  statable stats --range 2026-05-01..2026-05-31 --compare 2026-04-01..2026-04-30\n" +
			"  statable stats --json | jq .visitors",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			filters, err := q.parseFilters()
			if err != nil {
				return err
			}

			resp, err := rt.runQuery(cmd.Context(), query.Spec{
				Metrics:   q.metrics,
				DateRange: q.effectiveRange(cmd, rt),
				Filters:   filters,
				Compare:   q.compare,
			})
			if err != nil {
				return err
			}
			if len(resp.Results) == 0 {
				return clierr.Fail(clierr.CodeServer, "the server returned no totals")
			}

			row := resp.Results[0]
			rec := make(output.Record, 0, len(q.metrics)*3)
			anyCompare := false
			for _, m := range q.metrics {
				v, ok := row.Metrics[m]
				if !ok {
					continue
				}
				c, hasCompare := row.Compare[m]
				rec = append(rec, output.Field{
					Name:  m,
					Value: rawValue(v),
					Human: humanWithCompare(formatMetric(m, v), c, hasCompare),
				})
				if hasCompare {
					anyCompare = true
					rec = append(rec,
						output.Field{Name: m + previousSuffix, Value: c.Value, OmitHuman: true},
						output.Field{Name: m + changeSuffix, Value: c.Change, OmitHuman: true},
					)
				}
			}
			if len(rec) == 0 {
				// Exiting 0 with no output looks like success with no answer.
				// If not one requested metric came back, say so.
				return clierr.Failf(clierr.CodeServer,
					"the server returned none of the metrics that were asked for: %s",
					strings.Join(q.metrics, ", "))
			}
			if pair := resp.ResolvedRange(); len(pair) > 0 {
				if cmp := resp.ResolvedCompareRange(); len(cmp) > 0 {
					rt.Out.Note("period: %s, compared with %s",
						query.DescribeRange(pair), query.DescribeRange(cmp))
				} else {
					rt.Out.Note("period: %s", query.DescribeRange(pair))
				}
			}
			// A comparison that was asked for and did not arrive is worth
			// saying out loud: two dimensions compute no comparison at all,
			// and silence there reads as "nothing changed".
			if q.compare != "" && !anyCompare {
				rt.Out.Warn("the server returned no comparison for this query")
			}
			return rt.Out.EmitRecord(rec)
		},
	}
	q.register(cmd, "7d")
	q.registerMetrics(cmd, []string{"visitors", "pageviews", "bounce_rate", "visit_duration"})
	return cmd
}

// rawValue keeps a metric numeric for machine output instead of turning it
// into the formatted string a person reads.
func rawValue(v any) any {
	if f, ok := numeric(v); ok {
		return f
	}
	return v
}
