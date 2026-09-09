package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
)

func newQueryCmd() *cobra.Command {
	var q queryFlags
	var dimension string
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Any query the API can answer",
		Long: "The full surface of POST /query, for the questions the shorter commands\n" +
			"do not cover.\n\n" +
			"The shape of the answer follows the dimension: none gives totals, a time\n" +
			"bucket gives a series, anything else gives a ranked breakdown.\n\n" +
			"Examples:\n" +
			"  statable query --metric visitors,pageviews --range 7d\n" +
			"  statable query --metric visitors --dimension time:day --filter country=US\n" +
			"  statable query --metric visitors --dimension visit:utm_campaign --limit 25\n" +
			"  statable query --metric visitors --dimension event:props:plan --filter event=Signup",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			filters, err := q.parseFilters()
			if err != nil {
				return err
			}

			isBreakdown := dimension != "" && !query.TimeDimensions[dimension]
			spec := query.Spec{
				Metrics:   q.metrics,
				DateRange: q.effectiveRange(cmd, rt),
				Dimension: dimension,
				Filters:   filters,
				Compare:   q.compare,
			}
			// limit and offset are rejected outright on totals and series, so
			// they are only attached when the shape can carry them.
			if isBreakdown {
				spec.Limit, spec.HasLimit = q.limit, cmd.Flags().Changed("limit")
				spec.Offset, spec.HasOffset = q.offset, cmd.Flags().Changed("offset")
			} else if cmd.Flags().Changed("limit") || cmd.Flags().Changed("offset") {
				spec.HasLimit = cmd.Flags().Changed("limit")
				spec.HasOffset = cmd.Flags().Changed("offset")
			}

			resp, err := rt.runQuery(cmd.Context(), spec)
			if err != nil {
				return err
			}

			human := rt.Ctx.HumanOutput()
			if pair := resp.ResolvedRange(); len(pair) > 0 {
				rt.Out.Note("period: %s", query.DescribeRange(pair))
			}

			// Totals: one row, so it reads as a record rather than a table.
			if dimension == "" {
				if len(resp.Results) == 0 {
					return clierr.Fail(clierr.CodeServer, "the server returned no totals")
				}
				row := resp.Results[0]
				rec := make(output.Record, 0, len(q.metrics)*3)
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
						rec = append(rec,
							output.Field{Name: m + previousSuffix, Value: c.Value, OmitHuman: true},
							output.Field{Name: m + changeSuffix, Value: c.Change, OmitHuman: true},
						)
					}
				}
				if len(rec) == 0 {
					// An empty record renders as two bare newlines in CSV,
					// which a reader accepts as one empty row. Saying nothing
					// came back is the honest answer.
					return clierr.Failf(clierr.CodeServer,
						"the server returned none of the metrics that were asked for: %s",
						strings.Join(q.metrics, ", "))
				}
				return rt.Out.EmitRecord(rec)
			}

			t := output.Table{Columns: []string{dimensionKey(dimension)}}
			for _, m := range q.metrics {
				t.Columns = append(t.Columns, m)
				t.Right = append(t.Right, len(t.Columns)-1)
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
			// The readable name must be reachable from `query` too, or the
			// same request answers differently depending on which command
			// made it.
			if !human && query.GeoDimensions[dimension] {
				t.Columns = append(t.Columns, "label")
			}
			for _, r := range resp.Results {
				value := dimensionValue(r.Dimensions, dimension)
				label := r.Labels[dimension]
				if human && label != "" {
					value = label
				}
				row := []string{value}
				for _, m := range q.metrics {
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
				if !human && query.GeoDimensions[dimension] {
					row = append(row, label)
				}
				t.Rows = append(t.Rows, row)
			}
			if err := rt.Out.Emit(t); err != nil {
				return err
			}
			if m := resp.Meta; m != nil && m.HasMore {
				rt.Out.Note("showing %d of %d; next page: --offset %d",
					len(resp.Results), m.Total, m.Offset+len(resp.Results))
			}
			return nil
		},
	}
	q.register(cmd, "7d")
	q.registerMetrics(cmd, []string{"visitors"})
	q.registerPaging(cmd)
	cmd.Flags().StringVarP(&dimension, "dimension", "d", "",
		"one dimension: a time bucket such as time:day, or a breakdown such as event:page")
	return cmd
}
