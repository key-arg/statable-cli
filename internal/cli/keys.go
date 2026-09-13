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

func newKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "The API keys on this account",
		Long: "The account's active API keys.\n\n" +
			"The secret is never returned by the API, and this never prints one: a\n" +
			"listing shows the prefix, which is enough to tell two keys apart and\n" +
			"useless to anyone who copies it.\n\n" +
			"An empty site column means the key reaches every site the account owns.\n" +
			"An empty expiry means it never expires.\n\n" +
			"Examples:\n" +
			"  statable keys\n" +
			"  statable keys --json | jq -r '.[] | select(.expires_at == null) | .name'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := client.Get(cmd.Context(), "/keys", nil)
			rt.traceResponse(resp)
			if err != nil {
				return err
			}
			var out api.KeysResponse
			if err := api.DecodeInto(resp, &out); err != nil {
				return err
			}
			if len(out.Keys) == 0 {
				rt.Out.Note("this account has no active keys")
			}

			human := rt.Ctx.HumanOutput()
			t := output.Table{
				Columns:   []string{"id", "name", "prefix", "scopes", "site_id", "last_used_at", "expires_at", "created_at"},
				Right:     []int{0},
				HumanOmit: []int{7},
			}
			if !human {
				t.Numeric = []int{0}
			}
			for _, k := range out.Keys {
				site := ""
				if k.WebsiteID != nil {
					site = strconv.FormatInt(*k.WebsiteID, 10)
				}
				t.Rows = append(t.Rows, []string{
					strconv.FormatInt(k.ID, 10), k.Name, k.Prefix, k.Scopes, site,
					humanTime(human, k.LastUsedAt), humanTime(human, k.ExpiresAt),
					humanTime(human, &k.CreatedAt),
				})
			}
			return rt.Out.Emit(t)
		},
	}
	cmd.AddCommand(newKeyEventsCmd(), newKeysCreateCmd(), newKeysRotateCmd(), newKeysRevokeCmd())
	return cmd
}

// humanTime renders an optional instant. A person gets a short form; a machine
// gets what the server sent, and an absent value stays empty rather than
// becoming a zero date.
func humanTime(human bool, s *string) string {
	if s == nil || *s == "" {
		return ""
	}
	if human {
		return shortInstant(*s)
	}
	return *s
}

func newKeyEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events <id>",
		Short: "What has happened to one key",
		Long: "The lifecycle trail of one key: when it was created, used, rotated\n" +
			"or revoked, and from where.\n\n" +
			"Take the id from `statable keys`.\n\n" +
			"Examples:\n" +
			"  statable keys events 42",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil || id <= 0 {
				return clierr.Failf("INVALID_KEY_ID",
					"%q is not a key id; run `statable keys` to see them", args[0]).
					WithExit(clierr.ExitUsage)
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := client.Get(cmd.Context(), fmt.Sprintf("/keys/%d/events", id), nil)
			rt.traceResponse(resp)
			if err != nil {
				return err
			}
			var out api.KeyEventsResponse
			if err := api.DecodeInto(resp, &out); err != nil {
				return err
			}
			if len(out.Events) == 0 {
				rt.Out.Note("no events recorded for key %d", id)
			}

			human := rt.Ctx.HumanOutput()
			t := output.Table{
				Columns: []string{"id", "event", "ip", "created_at"},
				Right:   []int{0},
			}
			if !human {
				t.Numeric = []int{0}
			}
			for _, e := range out.Events {
				ip := ""
				if e.IP != nil {
					ip = *e.IP
				}
				t.Rows = append(t.Rows, []string{
					strconv.FormatInt(e.ID, 10), e.Event, ip,
					humanTime(human, &e.CreatedAt),
				})
			}
			return rt.Out.Emit(t)
		},
	}
}
