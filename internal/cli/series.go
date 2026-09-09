package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
	"github.com/key-arg/statable-cli/internal/spark"
)

func newSeriesCmd() *cobra.Command {
	var q queryFlags
	var by string
	cmd := &cobra.Command{
		Use:   "series",
		Short: "One metric over time, bucket by bucket",
		Long: "One metric over time.\n\n" +
			"A terminal also gets a sparkline. The scale starts at zero, so a flat\n" +
			"week looks flat instead of dramatic.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			filters, err := q.parseFilters()
			if err != nil {
				return err
			}
			marker, err := q.markerOrError()
			if err != nil {
				return err
			}
			if len(q.metrics) != 1 {
				return clierr.Fail("ONE_METRIC",
					"a series draws one metric at a time",
					"statable series --metric visitors")
			}
			metric := q.metrics[0]

			dim := "time:" + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(by)), "time:")
			if !query.TimeDimensions[dim] {
				return clierr.Failf("INVALID_INTERVAL",
					"unknown bucket %q; use minute, hour, day, week or month", by).
					WithExit(clierr.ExitUsage)
			}
			resp, err := rt.runQuery(cmd.Context(), query.Spec{
				Metrics:   q.metrics,
				DateRange: q.effectiveRange(cmd, rt),
				Dimension: dim,
				Filters:   filters,
				Compare:   q.compare,
			})
			if err != nil {
				return err
			}

			human := rt.Ctx.HumanOutput()
			t := output.Table{Columns: []string{dimensionKey(dim), metric}, Right: []int{1}}
			if !human {
				t.Numeric = []int{1}
				// The compare is requested and billed for; dropping it here
				// while stats, top and query all carry it made one command
				// the odd one out for every scripted caller.
				if q.compare != "" {
					for _, c := range compareColumns(metric) {
						t.Columns = append(t.Columns, c)
						t.Numeric = append(t.Numeric, len(t.Columns)-1)
					}
				}
			}
			values := make([]float64, 0, len(resp.Results))
			for _, r := range resp.Results {
				bucket := dimensionValue(r.Dimensions, dim)
				v := r.Metrics[metric]
				if f, ok := numeric(v); ok {
					values = append(values, f)
				} else {
					values = append(values, 0)
				}
				c, hasCompare := r.Compare[metric]
				row := []string{bucket}
				if human {
					row = append(row, humanWithCompare(formatMetric(metric, v), c, hasCompare))
				} else {
					row = append(row, rawMetric(v))
					if q.compare != "" {
						row = append(row, compareCells(c, hasCompare)...)
					}
				}
				t.Rows = append(t.Rows, row)
			}

			if pair := resp.ResolvedRange(); len(pair) > 0 {
				rt.Out.Note("period: %s", query.DescribeRange(pair))
			}
			if len(t.Rows) == 0 {
				rt.Out.Note("no buckets for this period")
			}
			if err := rt.Out.Emit(t); err != nil {
				return err
			}
			// The sparkline is a reading aid for a person looking at a
			// terminal. Piped output already drops the header for the same
			// reason: a chart is noise to awk, and leaving it in would make
			// the last line of the stream something no parser expects.
			if human && rt.Ctx.StdoutTTY && len(values) > 1 {
				rt.Out.Printf("\n%s\n", spark.Render(values, marker))
			}
			return nil
		},
	}
	q.register(cmd, "30d")
	q.registerMetrics(cmd, []string{"visitors"})
	q.registerMarker(cmd)
	cmd.Flags().StringVar(&by, "by", "day", "bucket: minute, hour, day, week or month")
	return cmd
}
