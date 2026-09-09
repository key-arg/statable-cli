package cli

import (
	"fmt"
	"math"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
)

func newCheckCmd() *cobra.Command {
	var q queryFlags
	var metric string
	var min, max float64

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Assert a metric is within bounds, for CI",
		Long: "Read one metric and compare it against a bound.\n\n" +
			"The exit code is the answer: 0 when the metric is within bounds, 3 when it\n" +
			"is not, and the usual 1 or 2 when the check could not be run at all. That\n" +
			"third code is the point: a pipeline can tell \"traffic dropped\" apart from\n" +
			"\"the tool is broken\", which one shared failure code makes impossible.\n\n" +
			"Examples:\n" +
			"  statable check --metric visitors --range 7d --min 100\n" +
			"  statable check --metric bounce_rate --max 60\n" +
			"  statable check --metric visitors --min 10 --filter page~/pricing",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())

			hasMin := cmd.Flags().Changed("min")
			hasMax := cmd.Flags().Changed("max")
			if !hasMin && !hasMax {
				return clierr.Fail("NO_BOUND",
					"a check needs a bound to compare against",
					"statable check --metric visitors --min 100",
					"statable check --metric bounce_rate --max 60").
					WithExit(clierr.ExitUsage)
			}
			for _, b := range []struct {
				set  bool
				name string
				v    float64
			}{{hasMin, "--min", min}, {hasMax, "--max", max}} {
				if !b.set {
					continue
				}
				// A NaN bound makes every comparison false, so the check would
				// pass whatever the number was: a deploy gate that can never
				// fail is worse than no gate. An infinite bound is nonsense in
				// the other direction.
				if math.IsNaN(b.v) || math.IsInf(b.v, 0) {
					return clierr.Failf("NO_BOUND",
						"%s needs a real number, not %v", b.name, b.v).
						WithExit(clierr.ExitUsage)
				}
			}
			if hasMin && hasMax && min > max {
				return clierr.Failf("NO_BOUND",
					"the lower bound %g is above the upper bound %g", min, max).
					WithExit(clierr.ExitUsage)
			}

			// -m means a list everywhere else, and a check judges one number.
			// Silently trying "visitors,pageviews" as a metric name gave an
			// unknown-metric error that named a string the user did not think
			// they had typed.
			if strings.Contains(metric, ",") {
				return clierr.Failf("ONE_METRIC",
					"a check judges one metric; %q looks like a list", metric).
					WithExit(clierr.ExitUsage)
			}

			filters, err := q.parseFilters()
			if err != nil {
				return err
			}

			resp, err := rt.runQuery(cmd.Context(), query.Spec{
				Metrics:   []string{metric},
				DateRange: q.effectiveRange(cmd, rt),
				Filters:   filters,
			})
			if err != nil {
				return err
			}
			if len(resp.Results) == 0 {
				return clierr.Fail(clierr.CodeServer, "the server returned no totals to check")
			}

			raw, present := resp.Results[0].Metrics[metric]
			if !present {
				return clierr.Failf(clierr.CodeServer,
					"the server did not return %s for this window", metric)
			}
			value, ok := numeric(raw)
			if !ok || math.IsNaN(value) {
				// A rate can be null when it is undefined for the window.
				// Treating that as zero would silently pass or fail a check on
				// a number that does not exist.
				return clierr.Failf(clierr.CodeServer,
					"%s is undefined for this window, so there is nothing to check", metric)
			}

			within := true
			var because string
			switch {
			case hasMin && value < min:
				within, because = false, fmt.Sprintf("below the minimum of %s", trimFloat(min))
			case hasMax && value > max:
				within, because = false, fmt.Sprintf("above the maximum of %s", trimFloat(max))
			}

			rec := output.Record{
				{Name: "metric", Value: metric},
				{Name: "value", Value: value, Human: formatMetric(metric, raw)},
			}
			// The bound is shown on a pass too: a CI log that says only
			// "within bounds" cannot answer "what gate did this clear?".
			if hasMin {
				rec = append(rec, output.Field{Name: "min", Value: min, Human: trimFloat(min)})
			}
			if hasMax {
				rec = append(rec, output.Field{Name: "max", Value: max, Human: trimFloat(max)})
			}
			verdict := "within bounds"
			if !within {
				verdict = because
			}
			rec = append(rec, output.Field{Name: "ok", Value: within, Human: verdict})

			if pair := resp.ResolvedRange(); len(pair) > 0 {
				rt.Out.Note("period: %s", query.DescribeRange(pair))
			}
			if rerr := rt.Out.EmitRecord(rec); rerr != nil {
				return rerr
			}
			if !within {
				// The record above is the whole report, so the renderer must
				// contribute only the code.
				return clierr.Silent(clierr.ExitThresholdNotMet)
			}
			return nil
		},
	}

	q.registerWithoutCompare(cmd, "7d")
	cmd.Flags().StringVarP(&metric, "metric", "m", "visitors", "the metric to check")
	cmd.Flags().Float64Var(&min, "min", 0, "fail when the metric is below this")
	cmd.Flags().Float64Var(&max, "max", 0, "fail when the metric is above this")
	return cmd
}

// trimFloat prints a bound without a trailing ".000000".
func trimFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
