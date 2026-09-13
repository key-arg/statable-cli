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

// settingsGroups are the five settings endpoints, each fetched on its own.
// They are separate calls on the server, so asking for all five costs five
// requests; naming one asks for one.
var settingsGroups = []string{"tracking", "hostnames", "countries", "blocked-ips", "public-dashboard"}

func newSettingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings [group]",
		Short: "How a site is configured to collect",
		Long: "What the site collects, and what it refuses.\n\n" +
			"Five separate settings, each its own call on the server. With no\n" +
			"argument all five are fetched and shown together; naming one fetches\n" +
			"only that one, which is what a script wants.\n\n" +
			"Groups: " + strings.Join(settingsGroups, ", ") + "\n\n" +
			"An empty allow list is not a closed door: it means everything that is\n" +
			"not blocked. That reads the opposite way round to most allow lists, and\n" +
			"it is the server's rule, not this program's.\n\n" +
			"Examples:\n" +
			"  statable settings\n" +
			"  statable settings tracking\n" +
			"  statable settings --json | jq -r '.[] | select(.setting == \"blocked-ips\") | .value'",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: settingsGroups,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			want := settingsGroups
			if len(args) == 1 {
				g := strings.ToLower(strings.TrimSpace(args[0]))
				if !validSettingsGroup(g) {
					return unknownSettingsGroup(g)
				}
				want = []string{g}
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}

			t := output.Table{Columns: []string{"setting", "value"}}
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				for _, g := range want {
					resp, rerr := client.Get(cmd.Context(),
						fmt.Sprintf("/sites/%d/settings/%s", site.SiteID, g), nil)
					rt.traceResponse(resp)
					if rerr != nil {
						return explainWriteDisabled(rerr, "site settings")
					}
					rows, derr := settingRows(g, resp)
					if derr != nil {
						return derr
					}
					t.Rows = append(t.Rows, rows...)
				}
				return nil
			})
			if err != nil {
				return err
			}
			return rt.Out.Emit(t)
		},
	}
	cmd.AddCommand(newSettingsSetCmd())
	return cmd
}

func validSettingsGroup(g string) bool {
	for _, s := range settingsGroups {
		if s == g {
			return true
		}
	}
	return false
}

// settingRows turns one group's response into rows. Each group has its own
// shape, so each is decoded into its own type rather than into a map: a typo
// in a field name then fails to compile instead of printing an empty cell.
func settingRows(group string, resp *api.Response) ([][]string, error) {
	switch group {
	case "tracking":
		var v api.TrackingSettings
		if err := api.DecodeInto(resp, &v); err != nil {
			return nil, err
		}
		// enabled is the list of feature ids that are on; features is the
		// whole catalogue with a flag each. Showing both would repeat the
		// same fact twice, so the row names what is on and the catalogue row
		// names what could be.
		available := make([]string, 0, len(v.Features))
		for _, f := range v.Features {
			available = append(available, f.ID)
		}
		return [][]string{
			{"tracking.bundle", v.Bundle},
			{"tracking.version", strconv.Itoa(v.Version)},
			{"tracking.enabled", strings.Join(v.Enabled, ", ")},
			{"tracking.available", strings.Join(available, ", ")},
		}, nil
	case "hostnames":
		var v api.HostnameSettings
		if err := api.DecodeInto(resp, &v); err != nil {
			return nil, err
		}
		return [][]string{
			{"hostnames.allowed", strings.Join(v.Allowed, ", ")},
			{"hostnames.blocked", strings.Join(v.Blocked, ", ")},
		}, nil
	case "countries":
		var v api.CountrySettings
		if err := api.DecodeInto(resp, &v); err != nil {
			return nil, err
		}
		return [][]string{
			{"countries.allowed", strings.Join(api.Codes(v.Allowed), ", ")},
			{"countries.blocked", strings.Join(api.Codes(v.Blocked), ", ")},
		}, nil
	case "blocked-ips":
		var v api.BlockedIPSettings
		if err := api.DecodeInto(resp, &v); err != nil {
			return nil, err
		}
		return [][]string{{"blocked-ips", strings.Join(v.BlockedIPs, ", ")}}, nil
	case "public-dashboard":
		var v api.PublicDashboardSettings
		if err := api.DecodeInto(resp, &v); err != nil {
			return nil, err
		}
		return [][]string{{"public-dashboard.enabled", strconv.FormatBool(v.Enabled)}}, nil
	}
	return nil, unknownSettingsGroup(group)
}

func unknownSettingsGroup(g string) error {
	return clierr.Failf(clierr.CodeInvalidFormat,
		"unknown settings group %q; use one of %s, or omit it for all of them",
		g, strings.Join(settingsGroups, ", ")).WithExit(clierr.ExitUsage)
}
