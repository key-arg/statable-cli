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

func newKeysCreateCmd() *cobra.Command {
	var name string
	var scopes []string
	var site string
	var expiresIn int
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Mint an API key",
		Long: "Create an API key and print it once.\n\n" +
			"The secret is returned exactly once, by the server, and never again by\n" +
			"anything. If it is lost the key has to be rotated. In a terminal it is\n" +
			"printed alone so it can be copied; anywhere else it is a field in the\n" +
			"payload, so `--json | jq -r .token` is the way to capture it.\n\n" +
			"Scopes default to read alone. Ask only for what the key will do:\n" +
			"  " + strings.Join(api.KeyScopes, ", ") + "\n\n" +
			"A key expires after 90 days unless --expires-in says otherwise. There\n" +
			"is no way to ask for one that never expires, so the expiry is printed\n" +
			"with the key rather than left to be discovered when it stops working.\n\n" +
			"Examples:\n" +
			"  statable keys create ci\n" +
			"  statable keys create deploy --scope read --scope sites:write\n" +
			"  statable keys create tmp --expires-in 30\n" +
			"  statable keys create ci --json | jq -r .token",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			body := api.CreateKeyRequest{Name: strings.TrimSpace(args[0])}
			if body.Name == "" {
				return clierr.Fail("INVALID_NAME", "a key needs a name").
					WithExit(clierr.ExitUsage)
			}
			for _, s := range scopes {
				s = strings.TrimSpace(s)
				if !validScope(s) {
					return clierr.Failf(clierr.CodeInvalidFormat,
						"unknown scope %q; the API knows %s", s, strings.Join(api.KeyScopes, ", ")).
						WithExit(clierr.ExitUsage)
				}
				body.Scopes = append(body.Scopes, s)
			}
			if cmd.Flags().Changed("expires-in") {
				if expiresIn < 1 {
					return clierr.Failf("INVALID_EXPIRY",
						"--expires-in counts days and must be at least 1, got %d", expiresIn).
						WithExit(clierr.ExitUsage)
				}
				body.ExpiresInDays = &expiresIn
			}

			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			// A key can be scoped to one site. That is resolved the usual way,
			// but only when asked for: an unscoped key is the default.
			if cmd.Flags().Changed("for-site") {
				rt.Proj.Site = site
				s, serr := rt.resolveSite(cmd.Context())
				if serr != nil {
					return serr
				}
				body.WebsiteID = &s.SiteID
			}

			resp, err := client.Post(cmd.Context(), "/keys", body)
			rt.traceResponse(resp)
			if err != nil {
				return explainWriteDisabled(err, "minting a key")
			}
			var out api.CreatedKey
			if err := api.DecodeInto(resp, &out); err != nil {
				return err
			}
			if out.Token == "" {
				return clierr.Fail(clierr.CodeServer,
					"the server created the key but returned no secret, and it cannot be shown later")
			}
			rt.Out.Warn("this is the only time the secret is shown; store it now")
			// The expiry the server actually assigned, not the one that was
			// asked for: they differ whenever --expires-in is omitted.
			if out.ExpiresAt != nil {
				rt.Out.Note("this key expires on %s", shortInstant(*out.ExpiresAt))
			}
			var expires any
			if out.ExpiresAt != nil {
				expires = *out.ExpiresAt
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "token", Value: out.Token, Human: out.Token},
				{Name: "id", Value: out.ID, OmitHuman: true},
				{Name: "name", Value: out.Name, OmitHuman: true},
				{Name: "prefix", Value: out.Prefix, OmitHuman: true},
				{Name: "scopes", Value: out.Scopes, OmitHuman: true},
				{Name: "expires_at", Value: expires, OmitHuman: true},
			})
		},
	}
	cmd.Flags().StringArrayVar(&scopes, "scope", nil,
		"permission to grant; repeat for more. Defaults to read")
	cmd.Flags().StringVar(&site, "for-site", "", "scope the key to one site")
	cmd.Flags().IntVar(&expiresIn, "expires-in", 0,
		"days until the key expires; omitted means the server's 90")
	_ = name
	return cmd
}

func validScope(s string) bool {
	for _, k := range api.KeyScopes {
		if k == s {
			return true
		}
	}
	return false
}

func newKeysRotateCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate <id>",
		Short: "Replace a key's secret",
		Long: "Issue a fresh secret for a key, keeping its name and permissions.\n\n" +
			"The old secret stops working immediately. Anything still using it --\n" +
			"a CI job, a cron entry, another machine -- breaks until it is updated,\n" +
			"which is why this asks first.\n\n" +
			"A key cannot rotate itself, either; the server answers 409\n" +
			"self_modification. Rotate it with a different key.\n\n" +
			"Examples:\n" +
			"  statable keys rotate 42\n" +
			"  statable keys rotate 42 --yes --json | jq -r .token",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := keyID(args[0])
			if err != nil {
				return err
			}
			if err := rt.confirm(yes, fmt.Sprintf(
				"the current secret of key %d, and anything still using it stops working", id)); err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := client.Post(cmd.Context(), fmt.Sprintf("/keys/%d/rotate", id), nil)
			rt.traceResponse(resp)
			if err != nil {
				return explainWriteDisabled(err, "rotating a key")
			}
			var out api.CreatedKey
			if err := api.DecodeInto(resp, &out); err != nil {
				return err
			}
			if out.Token == "" {
				return clierr.Fail(clierr.CodeServer,
					"the server rotated the key but returned no secret")
			}
			rt.Out.Warn("this is the only time the new secret is shown; store it now")
			return rt.Out.EmitRecord(output.Record{
				{Name: "token", Value: out.Token, Human: out.Token},
				{Name: "id", Value: out.ID, OmitHuman: true},
				{Name: "prefix", Value: out.Prefix, OmitHuman: true},
			})
		},
	}
	registerConfirm(cmd, &yes)
	return cmd
}

func newKeysRevokeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <id>",
		Aliases: []string{"delete"},
		Short:   "Revoke an API key",
		Long: "Revoke a key. It stops working immediately and cannot be restored.\n\n" +
			"A key cannot revoke itself: the server answers 409 self_modification.\n" +
			"To retire the key you are holding, use another one, or mint a\n" +
			"replacement first and authenticate with that.\n\n" +
			"Examples:\n" +
			"  statable keys revoke 42\n" +
			"  statable keys revoke 42 --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := keyID(args[0])
			if err != nil {
				return err
			}
			if err := rt.confirm(yes, fmt.Sprintf("key %d, permanently", id)); err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := client.Delete(cmd.Context(), fmt.Sprintf("/keys/%d", id))
			rt.traceResponse(resp)
			if err != nil {
				return explainWriteDisabled(err, "revoking a key")
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "revoked",
					Human: fmt.Sprintf("key %d is revoked", id)},
				{Name: "id", Value: id, OmitHuman: true},
			})
		},
	}
	registerConfirm(cmd, &yes)
	return cmd
}

func keyID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, clierr.Failf("INVALID_KEY_ID",
			"%q is not a key id; run `statable keys` to see them", s).
			WithExit(clierr.ExitUsage)
	}
	return id, nil
}
