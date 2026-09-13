package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

func newSitesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sites",
		Short: "List the sites this key can read",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			// This command's whole job is the listing, so it always asks the
			// server rather than answering from the cache it maintains for
			// everything else. It also refreshes that cache as a side effect,
			// which makes `statable sites` the documented repair for a listing
			// that has gone out of date.
			sites, err := rt.refreshSites(cmd.Context())
			if err != nil {
				return err
			}

			// An empty listing is a true answer, so it exits 0 and JSON gets
			// []. But in a terminal it printed nothing at all, and silence
			// does not distinguish "no sites" from "the command did nothing".
			if len(sites) == 0 {
				rt.Out.Note("this key can read no sites; create one at https://statable.com")
			}

			def := rt.loadSettings().DefaultSite
			if rt.Proj.Site != "" {
				def = rt.Proj.Site
			}

			// Everything /sites returns is carried through to a machine
			// caller. The terminal shows the four columns a person reads and
			// the rest is kept for JSON and CSV: hash is what a tracking
			// snippet needs, and stats_start_date is the earliest date any
			// query on that site can ask for.
			t := output.Table{
				Columns: []string{
					"site_id", "name", "timezone", "default",
					"hash", "created_at", "stats_start_date",
				},
				Right:     []int{0},
				Numeric:   []int{0},
				Bool:      []int{3},
				HumanOmit: []int{4, 5, 6},
			}
			// The default is matched once, not once per row: matchSite scans
			// the whole listing, so doing it inside the loop was quadratic and
			// could name a different site on each pass if two ever tied.
			var defID int64
			if def != "" {
				if m, ok := matchSite(sites, def); ok {
					defID = m.SiteID
				}
			} else if len(sites) == 1 {
				defID = sites[0].SiteID
			}
			for _, s := range sites {
				mark := ""
				if s.SiteID == defID {
					mark = "true"
				}
				start := ""
				if s.StatsStartDate != nil {
					start = *s.StatsStartDate
				}
				t.Rows = append(t.Rows, []string{
					strconv.FormatInt(s.SiteID, 10), s.Name, s.Timezone, mark,
					s.Hash, s.CreatedAt, start,
				})
			}
			return rt.Out.Emit(t)
		},
	}
	cmd.AddCommand(newSitesUseCmd(), newSitesCreateCmd(), newSitesEditCmd(), newSitesDeleteCmd())
	return cmd
}

func newSitesUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <site>",
		Short: "Remember a site as the default",
		Long: "Remember a site as the default for later commands.\n\n" +
			"The site may be named by id, by name, or by hostname. A --site flag or a\n" +
			"project file still wins over this, so a repository can pin its own site\n" +
			"without disturbing the one you chose here.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			sites, err := rt.fetchSites(cmd.Context())
			if err != nil {
				return err
			}
			s, ok := matchSite(sites, args[0])
			if !ok {
				return clierr.Failf(clierr.CodeSiteNotFound,
					"no site called %q; this key can read %s",
					args[0], strings.Join(siteNames(sites), ", "))
			}

			cur := rt.loadSettings()
			cur.DefaultSite = s.Name
			if err := rt.saveSettings(cur); err != nil {
				return err
			}

			// Only a real project file is worth warning about. The --site flag
			// overwrites the same field, so blaming a file that does not
			// exist sends the user looking for it.
			if rt.Proj.Path != "" && rt.Proj.Site != "" && rt.Proj.Site != s.Name {
				rt.Out.Warn("%s sets site=%q, which still wins over this default",
					rt.Proj.Path, rt.Proj.Site)
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "site_id", Value: s.SiteID, OmitHuman: true},
				{Name: "default_site", Value: s.Name,
					Human: fmt.Sprintf("default site is now %s", s.Name)},
			})
		},
	}
}
