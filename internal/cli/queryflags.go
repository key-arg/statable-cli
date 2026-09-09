package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/query"
	"github.com/key-arg/statable-cli/internal/spark"
)

// queryFlags are the options every reading command shares, so the same idea
// is spelled the same way everywhere.
type queryFlags struct {
	dateRange string
	filters   []string
	compare   string
	metrics   []string
	marker    string
	limit     int
	offset    int
}

func (q *queryFlags) register(cmd *cobra.Command, defaultRange string) {
	q.registerWithoutCompare(cmd, defaultRange)
	cmd.Flags().StringVar(&q.compare, "compare", "",
		"previous_period, or an explicit 2026-05-01..2026-05-31 of the same length as --range")
}

// registerWithoutCompare is for commands that do not read a comparison.
// Registering a flag a command ignores is the "setting that silently does
// nothing" this codebase refuses to ship anywhere else.
func (q *queryFlags) registerWithoutCompare(cmd *cobra.Command, defaultRange string) {
	q.registerRange(cmd, defaultRange)
	cmd.Flags().StringArrayVarP(&q.filters, "filter", "f", nil,
		"narrow the query, e.g. country=US or page~/blog; repeat to combine with AND")
}

// registerRange is for a command that takes a period and nothing else.
// Hiding a flag the command ignores is not enough: it is still accepted, and
// a filter that is typed, parsed and thrown away is the same defect as a
// setting that silently does nothing.
func (q *queryFlags) registerRange(cmd *cobra.Command, defaultRange string) {
	cmd.Flags().StringVarP(&q.dateRange, "range", "r", defaultRange,
		"7d, 30d, month, realtime, Nd, or 2026-06-01..2026-06-30")
}

func (q *queryFlags) registerMetrics(cmd *cobra.Command, def []string) {
	cmd.Flags().StringSliceVarP(&q.metrics, "metric", "m", def, "metrics to read")
}

func (q *queryFlags) registerMarker(cmd *cobra.Command) {
	cmd.Flags().StringVar(&q.marker, "marker", string(spark.Blocks),
		"sparkline glyphs: blocks, braille or ascii")
}

func (q *queryFlags) registerPaging(cmd *cobra.Command) {
	cmd.Flags().IntVarP(&q.limit, "limit", "n", 10, "how many rows")
	cmd.Flags().IntVar(&q.offset, "offset", 0, "skip this many rows")
}

// effectiveRange resolves the date range for one invocation.
//
// A project file may set `period`, and until now that setting was parsed,
// allowlisted, documented and then read by nobody: the exact "setting that
// silently does nothing" this codebase refuses to ship elsewhere. An explicit
// --range still wins over it.
func (q *queryFlags) effectiveRange(cmd *cobra.Command, rt *Runtime) string {
	if cmd.Flags().Changed("range") {
		return q.dateRange
	}
	if p := strings.TrimSpace(rt.Proj.Period); p != "" {
		return p
	}
	return q.dateRange
}

// parseFilters turns the repeated flag into API filters. Flags are AND'd and
// the comma-separated values inside one flag are OR'd, which matches how the
// API models them.
func (q *queryFlags) parseFilters() ([]api.Filter, error) {
	out := make([]api.Filter, 0, len(q.filters))
	for _, raw := range q.filters {
		f, err := query.ParseFilter(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func (q *queryFlags) markerOrError() (spark.Marker, error) {
	m := spark.Marker(q.marker)
	if !m.Valid() {
		return "", clierr.Failf(clierr.CodeInvalidFormat,
			"unknown marker %q; use blocks, braille or ascii", q.marker)
	}
	return m, nil
}

// runQuery validates the spec, resolves the site and sends the request.
//
// Validation comes first, before any network call. A misspelled metric should
// say so immediately rather than after two round trips, and it should never be
// reported as "name a site" just because listing sites happened to run first.
func (rt *Runtime) runQuery(ctx context.Context, spec query.Spec) (*api.QueryResponse, error) {
	req, err := spec.Build()
	if err != nil {
		return nil, err
	}
	client, err := rt.Client(ctx)
	if err != nil {
		return nil, err
	}
	var out api.QueryResponse
	send := func() error {
		resp, rerr := client.Post(ctx, "/query", req)
		rt.traceResponse(resp)
		if rerr != nil {
			return rerr
		}
		return api.DecodeInto(resp, &out)
	}
	if req.SiteID != 0 {
		// A Spec that already carries an id was built by a caller that knows
		// it, so no listing is needed. No CLI flag sets this; it exists for
		// the query package's own callers.
		if err := send(); err != nil {
			return nil, err
		}
	} else {
		// Otherwise the site is resolved through the listing, even when
		// --site is a bare number. Treating a numeric argument as an id
		// directly would save a round trip, but a site may be *named*
		// something like "12345", and then the shortcut silently reports on
		// a different property. Correctness outranks one saved request.
		err = rt.withSite(ctx, func(site api.Site) error {
			req.SiteID = site.SiteID
			return send()
		})
		if err != nil {
			return nil, err
		}
	}
	// A number that silently mixes two sources is a warning, not a note: by
	// this codebase's own rule, --quiet must not be able to hide something
	// that changes how the figure should be read.
	if out.HasImport {
		rt.Out.Warn("this window overlaps imported data, so the numbers blend imported and native traffic")
	}
	return &out, nil
}
