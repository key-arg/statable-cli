package cli

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

// snippetTypes are the widget builds the server will hand out, alongside the
// plain tracker that an empty type selects. Copied from widgetFileByType.
var snippetTypes = []string{"globe", "live-users", "map", "countries"}

func newSnippetCmd() *cobra.Command {
	var typ string
	cmd := &cobra.Command{
		Use:   "snippet",
		Short: "The install tag for a site",
		Long: "The script tag to put on a page.\n\n" +
			"With no --type this is the tracker. The widget builds draw something\n" +
			"and, on a hobby site, count as well -- there the widget is the tracker,\n" +
			"so do not install both.\n\n" +
			"Human output is the tag alone, so it pipes straight into a clipboard\n" +
			"or a file. The machine formats carry the script URL and the site id\n" +
			"beside it.\n\n" +
			"Examples:\n" +
			"  statable snippet\n" +
			"  statable snippet | pbcopy\n" +
			"  statable snippet --type globe\n" +
			"  statable snippet --json | jq -r .script_url",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			// Checked before anything is sent: the server answers an unknown
			// type with a 400 that does not say which ones exist.
			if typ != "" && !validSnippetType(typ) {
				return clierr.Failf(clierr.CodeInvalidFormat,
					"unknown snippet type %q; use one of %s, or omit --type for the tracker",
					typ, strings.Join(snippetTypes, ", ")).WithExit(clierr.ExitUsage)
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var sn api.Snippet
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				var q url.Values
				if typ != "" {
					q = url.Values{"type": {typ}}
				}
				resp, rerr := client.Get(cmd.Context(),
					fmt.Sprintf("/sites/%d/snippet", site.SiteID), q)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "the install tag")
				}
				return api.DecodeInto(resp, &sn)
			})
			if err != nil {
				return err
			}
			if strings.TrimSpace(sn.Snippet) == "" {
				return clierr.Fail(clierr.CodeServer,
					"the server returned no snippet")
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "snippet", Value: sn.Snippet, Human: sn.Snippet},
				{Name: "script_url", Value: sn.ScriptURL, OmitHuman: true},
				{Name: "site_id", Value: sn.SiteID, OmitHuman: true},
			})
		},
	}
	cmd.Flags().StringVar(&typ, "type", "",
		"widget build instead of the tracker: "+strings.Join(snippetTypes, ", "))
	return cmd
}

func validSnippetType(t string) bool {
	i := sort.SearchStrings(sortedSnippetTypes, t)
	return i < len(sortedSnippetTypes) && sortedSnippetTypes[i] == t
}

var sortedSnippetTypes = func() []string {
	out := append([]string(nil), snippetTypes...)
	sort.Strings(out)
	return out
}()
