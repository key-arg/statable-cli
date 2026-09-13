package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

// newSettingsSetCmd writes one settings group.
//
// Every one of these is a PUT, and the API means it: the body replaces the
// whole setting. There is no "add one hostname" -- give the list you want to
// end up with. The help says so on each subcommand, because getting it wrong
// silently empties a blocklist.
func newSettingsSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Change how a site collects",
		Long: "Change one settings group.\n\n" +
			"Each of these replaces the whole group. The list you give is the list\n" +
			"the site ends up with, so read the current one first if you mean to\n" +
			"add to it:\n\n" +
			"  statable settings hostnames\n" +
			"  statable settings set hostnames --allow example.com --allow www.example.com",
		RunE: func(cmd *cobra.Command, args []string) error { return helpOrUsage(cmd) },
	}
	cmd.AddCommand(
		newSetHostnamesCmd(), newSetCountriesCmd(),
		newSetBlockedIPsCmd(), newSetPublicDashboardCmd(), newSetTrackingCmd(),
	)
	return cmd
}

// putSetting is the shape every one of these shares.
func (r *Runtime) putSetting(cmd *cobra.Command, group string, body any, done string) error {
	client, err := r.Client(cmd.Context())
	if err != nil {
		return err
	}
	err = r.withSite(cmd.Context(), func(site api.Site) error {
		resp, rerr := client.Put(cmd.Context(),
			fmt.Sprintf("/sites/%d/settings/%s", site.SiteID, group), body)
		r.traceResponse(resp)
		if rerr != nil {
			return explainWriteDisabled(rerr, "site settings")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return r.Out.EmitRecord(output.Record{
		{Name: "status", Value: "ok", Human: done},
		{Name: "setting", Value: group, OmitHuman: true},
	})
}

func newSetHostnamesCmd() *cobra.Command {
	var allow, block []string
	cmd := &cobra.Command{
		Use:   "hostnames",
		Short: "Replace the hostname allow and block lists",
		Long: "Replace both hostname lists at once.\n\n" +
			"An empty allow list means everything that is not blocked, which reads\n" +
			"the opposite way round to most allow lists. Giving neither flag clears\n" +
			"both lists, and that is a real choice, so it has to be said with\n" +
			"--clear rather than by accident.\n\n" +
			"Examples:\n" +
			"  statable settings set hostnames --allow example.com --allow www.example.com\n" +
			"  statable settings set hostnames --block staging.example.com\n" +
			"  statable settings set hostnames --clear",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			if err := requireListIntent(cmd, "allow", "block"); err != nil {
				return err
			}
			return rt.putSetting(cmd, "hostnames",
				api.HostnameSettings{Allowed: allow, Blocked: block},
				fmt.Sprintf("hostnames replaced: %d allowed, %d blocked", len(allow), len(block)))
		},
	}
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "hostname to allow; repeat for more")
	cmd.Flags().StringArrayVar(&block, "block", nil, "hostname to block; repeat for more")
	cmd.Flags().Bool("clear", false, "empty both lists")
	return cmd
}

func newSetCountriesCmd() *cobra.Command {
	var allow, block []string
	cmd := &cobra.Command{
		Use:   "countries",
		Short: "Replace the country allow and block lists",
		Long: "Replace both country lists at once, by ISO 3166-1 alpha-2 code.\n\n" +
			"These are the same codes a country breakdown returns in machine\n" +
			"output, so a value from `statable top countries --json` drops straight\n" +
			"in.\n\n" +
			"Examples:\n" +
			"  statable settings set countries --block RU --block BY\n" +
			"  statable settings set countries --allow UA --allow PL\n" +
			"  statable settings set countries --clear",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			if err := requireListIntent(cmd, "allow", "block"); err != nil {
				return err
			}
			for _, c := range append(append([]string{}, allow...), block...) {
				if len(strings.TrimSpace(c)) != 2 {
					return clierr.Failf(clierr.CodeInvalidFormat,
						"%q is not a country code; use two letters, like UA", c).
						WithExit(clierr.ExitUsage)
				}
			}
			return rt.putSetting(cmd, "countries",
				api.CountryListRequest{Allowed: upperAll(allow), Blocked: upperAll(block)},
				fmt.Sprintf("countries replaced: %d allowed, %d blocked", len(allow), len(block)))
		},
	}
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "country code to allow; repeat for more")
	cmd.Flags().StringArrayVar(&block, "block", nil, "country code to block; repeat for more")
	cmd.Flags().Bool("clear", false, "empty both lists")
	return cmd
}

func upperAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToUpper(strings.TrimSpace(s)))
	}
	return out
}

func newSetBlockedIPsCmd() *cobra.Command {
	var ips []string
	cmd := &cobra.Command{
		Use:   "blocked-ips",
		Short: "Replace the IP blocklist",
		Long: "Replace the whole IP blocklist.\n\n" +
			"Traffic from these addresses is not recorded. This replaces the list,\n" +
			"so read the current one first if you mean to add to it.\n\n" +
			"Examples:\n" +
			"  statable settings set blocked-ips --ip 203.0.113.4 --ip 198.51.100.7\n" +
			"  statable settings set blocked-ips --clear",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			if err := requireListIntent(cmd, "ip"); err != nil {
				return err
			}
			// A bare array, not an object: apiPutBlockedIPsHandler does
			// json.Unmarshal into []string, and an object is a 400 every
			// time. The GET answers with an object, which is what made the
			// symmetry look safe.
			if ips == nil {
				ips = []string{}
			}
			return rt.putSetting(cmd, "blocked-ips", ips,
				fmt.Sprintf("blocklist replaced: %d addresses", len(ips)))
		},
	}
	cmd.Flags().StringArrayVar(&ips, "ip", nil, "address to block; repeat for more")
	cmd.Flags().Bool("clear", false, "empty the list")
	return cmd
}

func newSetPublicDashboardCmd() *cobra.Command {
	var on, off bool
	cmd := &cobra.Command{
		Use:   "public-dashboard",
		Short: "Publish or unpublish the site's stats",
		Long: "Make the site's dashboard world-readable, or stop.\n\n" +
			"Published means anyone with the address sees the numbers, without a\n" +
			"login. There is no middle setting, so this takes --on or --off and\n" +
			"refuses to guess.\n\n" +
			"Examples:\n" +
			"  statable settings set public-dashboard --on\n" +
			"  statable settings set public-dashboard --off",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			if on == off {
				return clierr.Fail("AMBIGUOUS",
					"say which: --on publishes the dashboard, --off takes it down").
					WithExit(clierr.ExitUsage)
			}
			state := "not published"
			if on {
				state = "published; anyone with the address can read it"
			}
			return rt.putSetting(cmd, "public-dashboard",
				api.PublicDashboardSettings{Enabled: on},
				"dashboard is "+state)
		},
	}
	cmd.Flags().BoolVar(&on, "on", false, "publish")
	cmd.Flags().BoolVar(&off, "off", false, "unpublish")
	return cmd
}

func newSetTrackingCmd() *cobra.Command {
	var features []string
	cmd := &cobra.Command{
		Use:   "tracking",
		Short: "Set what the installed script collects",
		Long: "Replace the tracking feature list.\n\n" +
			"This decides what the script on the page collects. Read the current\n" +
			"list first: this replaces it, and dropping a feature stops that data\n" +
			"being collected from the moment it takes effect.\n\n" +
			"  statable settings tracking\n\n" +
			"Examples:\n" +
			"  statable settings set tracking --feature outbound --feature scroll",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			if err := requireListIntent(cmd, "feature"); err != nil {
				return err
			}
			// features must be present and an array. A nil slice marshals to
			// null, which the server rejects as the field being missing.
			if features == nil {
				features = []string{}
			}
			return rt.putSetting(cmd, "tracking",
				map[string]any{"features": features},
				fmt.Sprintf("tracking features replaced: %s", strings.Join(features, ", ")))
		},
	}
	cmd.Flags().StringArrayVar(&features, "feature", nil, "feature to enable; repeat for more")
	cmd.Flags().Bool("clear", false, "collect nothing beyond the basics")
	return cmd
}

// requireListIntent refuses to send an empty replacement by accident.
//
// These are all PUTs, so omitting every flag would clear the setting. That is
// a legitimate thing to want and a terrible thing to do by mistake, so it has
// to be spelled --clear.
func requireListIntent(cmd *cobra.Command, flags ...string) error {
	for _, f := range flags {
		if cmd.Flags().Changed(f) {
			return nil
		}
	}
	if c, err := cmd.Flags().GetBool("clear"); err == nil && c {
		return nil
	}
	named := make([]string, 0, len(flags))
	for _, f := range flags {
		named = append(named, "--"+f)
	}
	return clierr.Failf("NOTHING_GIVEN",
		"this replaces the whole setting, and you gave nothing: use %s, or --clear to empty it",
		strings.Join(named, " or ")).WithExit(clierr.ExitUsage)
}
