package cli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
)

func newFunnelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "funnels",
		Short: "Saved funnel definitions",
		Long: "The funnels saved on this site.\n\n" +
			"Definitions are built in the dashboard under Custom Analytics; the API\n" +
			"only runs them. Take an id from here into `statable funnel <id>`.\n\n" +
			"Examples:\n" +
			"  statable funnels\n" +
			"  statable funnels --json | jq '.[].id'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var out api.FunnelsResponse
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				v := url.Values{"site_id": {strconv.FormatInt(site.SiteID, 10)}}
				resp, rerr := client.Get(cmd.Context(), "/funnels", v)
				rt.traceResponse(resp)
				if rerr != nil {
					return rerr
				}
				return api.DecodeInto(resp, &out)
			})
			if err != nil {
				return err
			}
			if len(out.Funnels) == 0 {
				rt.Out.Note("this site has no saved funnels; they are created in the dashboard under Custom Analytics")
			}
			human := rt.Ctx.HumanOutput()
			t := output.Table{
				Columns: []string{"id", "name", "steps", "scope", "strict_order", "created_at"},
				Right:   []int{0, 2},
				Bool:    []int{4},
			}
			if !human {
				t.Numeric = []int{0, 2}
			}
			for _, f := range out.Funnels {
				strict := ""
				if f.StrictOrder {
					strict = "true"
				}
				created := f.CreatedAt
				if human {
					created = shortTimestamp(created)
				}
				t.Rows = append(t.Rows, []string{
					strconv.FormatInt(f.ID, 10), f.Name,
					strconv.Itoa(len(f.Steps)), f.Scope, strict, created,
				})
			}
			return rt.Out.Emit(t)
		},
	}
	return cmd
}

func newFunnelCmd() *cobra.Command {
	var q queryFlags
	cmd := &cobra.Command{
		Use:   "funnel <id>",
		Short: "Run one saved funnel",
		Long: "Run a saved funnel over a period and print each step.\n\n" +
			"Two things to read correctly. The conversion rate is cumulative: it is\n" +
			"measured against everyone who entered, not against the step before.\n" +
			"Dropoff is the opposite and counts against the previous step alone.\n\n" +
			"The period decides which sessions are examined. It does not change how\n" +
			"long a visitor has to finish, which is one day, always.\n\n" +
			"Filters here accept session fields only, such as country, browser,\n" +
			"device, source or entry page. Event-level fields are rejected, because\n" +
			"the funnel's own steps already decide which events count.\n\n" +
			"Examples:\n" +
			"  statable funnel 45\n" +
			"  statable funnel 45 --range 7d --filter country=DE",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil || id <= 0 {
				return clierr.Failf("INVALID_FUNNEL_ID",
					"%q is not a funnel id; run `statable funnels` to see them", args[0]).
					WithExit(clierr.ExitUsage)
			}
			dr, err := query.ParseDateRange(q.effectiveRange(cmd, rt))
			if err != nil {
				return err
			}
			filters, err := q.parseFilters()
			if err != nil {
				return err
			}
			// The server rejects event-level filters here with a 400. Saying so
			// before the request costs nothing and names the actual rule,
			// which the server's message does not.
			for _, f := range filters {
				if funnelDisallowedField(f.Field) {
					return clierr.Failf(clierr.CodeInvalidFilter,
						"a funnel cannot filter on %s; its own steps decide which events count, "+
							"so only session fields such as country, browser, device or source apply",
						f.Field).WithExit(clierr.ExitUsage)
				}
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var rep api.FunnelReport
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				body := api.FunnelReportRequest{
					SiteID: site.SiteID, DateRange: dr, Filters: filters,
				}
				resp, rerr := client.Post(cmd.Context(),
					fmt.Sprintf("/funnels/%d/report", id), body)
				rt.traceResponse(resp)
				if rerr != nil {
					return rerr
				}
				return api.DecodeInto(resp, &rep)
			})
			if err != nil {
				return err
			}
			return renderFunnel(rt, rep)
		},
	}
	q.registerWithoutCompare(cmd, "30d")
	return cmd
}

// funnelDisallowedField names the event-level fields the API refuses on a
// funnel. The set is copied from the server's own map, which holds exactly
// these three and matches on the bare field name; widening it here by guessing
// at siblings would reject filters the server accepts.
func funnelDisallowedField(field string) bool {
	switch field {
	case "event", "page", "code":
		return true
	}
	return false
}

func renderFunnel(rt *Runtime, rep api.FunnelReport) error {
	// A saved funnel always has steps, so a report without them is not an
	// empty funnel: it is a response that did not answer. Exiting 0 with no
	// rows would look like success with nothing in it, which is the same
	// failure `stats` refuses when no requested metric comes back.
	if len(rep.Steps) == 0 {
		return clierr.Fail(clierr.CodeServer,
			"the server returned a funnel report with no steps",
			"statable funnels")
	}
	name := rep.Funnel.Name
	if name == "" {
		name = "funnel"
	}
	rt.Out.Note("%s: %s of %s visitors entered",
		name,
		formatMetric("visitors", float64(rep.Entering)),
		formatMetric("visitors", float64(rep.AllVisitors)))

	// The same split every other table follows: a person reads formatted text
	// and a machine gets raw numbers, so the rate carries a percent sign in a
	// terminal and stays a plain number in JSON and CSV.
	human := rt.Ctx.HumanOutput()
	t := output.Table{
		Columns: []string{"index", "step", "kind", "visitors", "conversion_rate", "dropoff"},
		Right:   []int{0, 3, 4, 5},
	}
	if !human {
		t.Numeric = []int{0, 3, 4, 5}
	}
	// The order is the server's. index is the position from zero and the rows
	// arrive in it, so nothing here sorts them.
	for _, s := range rep.Steps {
		visitors := strconv.FormatInt(s.Visitors, 10)
		rate := strconv.FormatFloat(s.ConversionRate, 'f', -1, 64)
		drop := strconv.FormatInt(s.Dropoff, 10)
		if human {
			visitors = formatMetric("visitors", float64(s.Visitors))
			rate = formatMetric("conversion_rate", s.ConversionRate)
			drop = formatMetric("visitors", float64(s.Dropoff))
		}
		t.Rows = append(t.Rows, []string{
			strconv.Itoa(s.Index), s.Name, s.Kind, visitors, rate, drop,
		})
	}
	return rt.Out.Emit(t)
}
