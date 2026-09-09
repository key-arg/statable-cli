package cli

import (
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/output"
)

func newNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "now",
		Short: "How many visitors are active right now",
		Long: "Unique visitors active in the last five minutes.\n\n" +
			"One number, so it fits a status bar or a shell prompt. For a live series\n" +
			"instead of a single figure, use `statable series --range realtime --by minute`:\n" +
			"the realtime window is the only one that buckets by minute, and the only\n" +
			"bucket it accepts.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var cv api.CurrentVisitors
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				q := url.Values{"site_id": {strconv.FormatInt(site.SiteID, 10)}}
				resp, rerr := client.Get(cmd.Context(), "/current-visitors", q)
				rt.traceResponse(resp)
				if rerr != nil {
					return rerr
				}
				return api.DecodeInto(resp, &cv)
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "visitors", Value: cv.Visitors,
					Human: formatMetric("visitors", float64(cv.Visitors))},
			})
		},
	}
}
