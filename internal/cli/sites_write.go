package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

func newSitesCreateCmd() *cobra.Command {
	var timezone string
	var hobby bool
	cmd := &cobra.Command{
		Use:   "create <url>",
		Short: "Create a site",
		Long: "Create a site and print the tag to install on it.\n\n" +
			"The URL must carry a scheme. The response already contains the install\n" +
			"tag, so there is no second call to make.\n\n" +
			"Examples:\n" +
			"  statable sites create https://example.com\n" +
			"  statable sites create https://example.com --timezone Europe/Kyiv\n" +
			"  statable sites create https://example.com --json | jq -r .snippet",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			url := strings.TrimSpace(args[0])
			if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				return clierr.Failf("INVALID_URL",
					"a site url needs a scheme; write https://%s", strings.TrimPrefix(url, "//")).
					WithExit(clierr.ExitUsage)
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := client.Post(cmd.Context(), "/sites", api.CreateSiteRequest{
				URL: url, Timezone: timezone, Hobby: hobby,
			})
			rt.traceResponse(resp)
			if err != nil {
				return explainWriteDisabled(err, "creating a site")
			}
			var out api.CreatedSite
			if err := api.DecodeInto(resp, &out); err != nil {
				return err
			}
			rt.Out.Note("created %s (site_id %d); the tag below goes on every page",
				out.Name, out.SiteID)
			return rt.Out.EmitRecord(output.Record{
				{Name: "snippet", Value: out.Snippet, Human: out.Snippet},
				{Name: "site_id", Value: out.SiteID, OmitHuman: true},
				{Name: "name", Value: out.Name, OmitHuman: true},
				{Name: "timezone", Value: out.Timezone, OmitHuman: true},
				{Name: "script_url", Value: out.ScriptURL, OmitHuman: true},
				{Name: "hash", Value: out.Hash, OmitHuman: true},
			})
		},
	}
	cmd.Flags().StringVar(&timezone, "timezone", "", "IANA zone, e.g. Europe/Kyiv; omitted means the server default")
	cmd.Flags().BoolVar(&hobby, "hobby", false, "request a hobby site")
	return cmd
}

func newSitesEditCmd() *cobra.Command {
	var url, timezone, weekStart string
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Change a site's url, timezone or week start",
		Long: "Change what can be changed about a site.\n\n" +
			"Only the flags you give are sent, so this never overwrites a setting\n" +
			"you did not mention. The site is the usual one: --site, the project\n" +
			"file, or the saved default.\n\n" +
			"Examples:\n" +
			"  statable sites edit --timezone Europe/Kyiv\n" +
			"  statable sites edit --week-start monday\n" +
			"  statable sites edit --site example.com --url https://www.example.com",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			var body api.PatchSiteRequest
			if cmd.Flags().Changed("url") {
				if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
					return clierr.Fail("INVALID_URL",
						"a site url needs a scheme, http:// or https://").
						WithExit(clierr.ExitUsage)
				}
				body.URL = &url
			}
			if cmd.Flags().Changed("timezone") {
				body.Timezone = &timezone
			}
			if cmd.Flags().Changed("week-start") {
				n, err := parseWeekStart(weekStart)
				if err != nil {
					return err
				}
				body.WeekStart = &n
			}
			if body.URL == nil && body.Timezone == nil && body.WeekStart == nil {
				return clierr.Fail("NOTHING_TO_CHANGE",
					"name at least one of --url, --timezone or --week-start").
					WithExit(clierr.ExitUsage)
			}

			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var site api.Site
			err = rt.withSite(cmd.Context(), func(s api.Site) error {
				site = s
				resp, rerr := client.Patch(cmd.Context(),
					fmt.Sprintf("/sites/%d", s.SiteID), body)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "changing a site")
				}
				return nil
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "ok", Human: fmt.Sprintf("%s updated", site.Name)},
				{Name: "site_id", Value: site.SiteID, OmitHuman: true},
			})
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "the site's address")
	cmd.Flags().StringVar(&timezone, "timezone", "", "IANA zone")
	cmd.Flags().StringVar(&weekStart, "week-start", "", "the first day of the week: a name, or 0 for Sunday through 6")
	return cmd
}

// parseWeekStart takes a day name or a number, because the API's 0-to-6 is
// unmemorable and getting it wrong silently shifts every weekly figure.
func parseWeekStart(s string) (int, error) {
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	t := strings.ToLower(strings.TrimSpace(s))
	for i, d := range days {
		if t == d || t == d[:3] {
			return i, nil
		}
	}
	if n, err := strconv.Atoi(t); err == nil && n >= 0 && n <= 6 {
		return n, nil
	}
	return 0, clierr.Failf("INVALID_WEEK_START",
		"%q is not a day; use a name like monday, or 0 for Sunday through 6 for Saturday", s).
		WithExit(clierr.ExitUsage)
}

func newSitesDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a site and everything recorded for it",
		Long: "Delete a site.\n\n" +
			"This removes the site and every event ever recorded for it. There is no\n" +
			"undo and no export afterwards, so take what you need first:\n\n" +
			"  statable query --metric visitors --dimension time:day --range 366d --format csv > backup.csv\n\n" +
			"Examples:\n" +
			"  statable sites delete --site old.example\n" +
			"  statable sites delete --site old.example --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			// The site is resolved before the question, so the question can
			// name it. "Delete the site?" is not something anyone can answer.
			site, err := rt.resolveSite(cmd.Context())
			if err != nil {
				return err
			}
			if err := rt.confirm(yes, fmt.Sprintf(
				"%s and every event recorded for it", site.Name)); err != nil {
				return err
			}
			resp, err := client.Delete(cmd.Context(), fmt.Sprintf("/sites/%d", site.SiteID))
			rt.traceResponse(resp)
			if err != nil {
				return explainWriteDisabled(err, "deleting a site")
			}
			// The cached listing now names a site that is gone.
			rt.dropSiteCache()
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "deleted",
					Human: fmt.Sprintf("%s is gone", site.Name)},
				{Name: "site_id", Value: site.SiteID, OmitHuman: true},
			})
		},
	}
	registerConfirm(cmd, &yes)
	return cmd
}
