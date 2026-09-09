package cli

import (
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/query"
)

func newPropsCmd() *cobra.Command {
	var q queryFlags
	cmd := &cobra.Command{
		Use:   "props",
		Short: "Custom property keys a site records",
		Long: "The custom properties a site has actually recorded.\n\n" +
			"This is discovery for the event:props:<key> breakdown, which otherwise\n" +
			"needs the key known in advance. Every entry is a key *and an event*,\n" +
			"because a property belongs to the event it was sent with: the same key\n" +
			"under two events is two entries. Take both into the next query, the\n" +
			"event as a filter and the key as the dimension.\n\n" +
			"Examples:\n" +
			"  statable props\n" +
			"  statable props --range 7d\n" +
			"  statable top event:props:plan --filter 'event=Signup'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			// The range is validated before anything is sent, so a bad one is
			// reported as a bad range and not as a failure to name a site.
			dr, err := query.ParseDateRange(q.effectiveRange(cmd, rt))
			if err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var out api.PropsResponse
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				v := url.Values{"site_id": {strconv.FormatInt(site.SiteID, 10)}}
				// date_range goes on the query string, so only the string
				// presets can travel here; an explicit pair is encoded the
				// same way it is written on the command line.
				v.Set("date_range", dateRangeParam(dr))
				resp, rerr := client.Get(cmd.Context(), "/props", v)
				rt.traceResponse(resp)
				if rerr != nil {
					return rerr
				}
				return api.DecodeInto(resp, &out)
			})
			if err != nil {
				return err
			}
			if len(out.Props) == 0 {
				rt.Out.Note("this site has recorded no custom properties in that period")
			}
			// The same human/machine split every other table follows: a
			// person reads grouped numbers and a readable date, a machine
			// gets the raw count and the timestamp exactly as it arrived.
			human := rt.Ctx.HumanOutput()
			t := output.Table{
				Columns: []string{"key", "event", "count", "first_seen"},
				Right:   []int{2},
			}
			if !human {
				t.Numeric = []int{2}
			}
			for _, p := range out.Props {
				count := strconv.FormatInt(p.Count, 10)
				seen := p.FirstSeen
				if human {
					count = formatMetric("events", float64(p.Count))
					seen = shortTimestamp(seen)
				}
				t.Rows = append(t.Rows, []string{p.Key, p.Event, count, seen})
			}
			return rt.Out.Emit(t)
		},
	}
	// Only a period. GET /props takes no filters — apiListPropsHandler reads
	// site_id and date_range and nothing else — so registering --filter would
	// accept a value and throw it away.
	q.registerRange(cmd, "30d")
	return cmd
}
