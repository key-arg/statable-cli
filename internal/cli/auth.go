package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/auth"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/execctx"
	"github.com/key-arg/statable-cli/internal/output"
)

// dashboardKeysURL is where a key is created.
const dashboardKeysURL = "https://statable.com/settings/api-keys"

func newAuthCmd(insecure *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the API key",
		// A parent command with no action of its own still has to answer a
		// machine caller with something parseable and a non-zero code, rather
		// than printing help and exiting 0 as if the invocation had worked.
		RunE: func(c *cobra.Command, args []string) error {
			return helpOrUsage(c)
		},
	}
	// Persistent on `auth` rather than on `login`, so both spellings work:
	// `statable auth --insecure-storage login` and `statable auth login
	// --insecure-storage`. It stays off the root, where it appeared in the
	// help of sixteen commands that never read it.
	cmd.PersistentFlags().BoolVar(insecure, "insecure-storage", false,
		"store the key in a plaintext file instead of the system keyring")
	cmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var nonInteractive bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an API key",
		Long: "Store a Statable API key.\n\n" +
			"With --key the key is taken from the flag. Without it, and on a terminal,\n" +
			"you are prompted. Anywhere else — a pipe, CI, an agent — the command prints\n" +
			"what to do next and exits rather than blocking on input nobody can type.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			flagKey := rt.FlagKey()

			// Stripe's rule: the non-interactive shape activates on its own
			// when stdin is not a terminal, so a script does not have to know
			// to ask for it.
			if flagKey == "" && (nonInteractive || rt.Ctx.LoginMode() == execctx.LoginNonInteractive) {
				return loginInstructions()
			}

			if flagKey != "" {
				rt.Out.Warn("a key given on the command line is visible in ps and stays in shell history; %s is safer",
					auth.EnvVar)
			}

			key := flagKey
			if key == "" {
				var err error
				key, err = promptForKey(rt)
				if err != nil {
					return err
				}
			}

			acct, err := verifyKey(cmd, rt, key)
			if err != nil {
				return err
			}

			src, err := rt.Store.Save(cmd.Context(), key)
			if err != nil {
				return err
			}

			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "ok", OmitHuman: true},
				{Name: "stored", Value: string(src),
					Human: fmt.Sprintf("signed in, key %s is in %s",
						auth.Redact(key), storageName(src))},
				{Name: "key", Value: auth.Redact(key), OmitHuman: true},
				{Name: "sites", Value: acct,
					Human: fmt.Sprintf("%d site(s) readable", acct)},
			})
		},
	}
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false,
		"print what to do next as JSON and exit instead of prompting")
	return cmd
}

func storageName(s auth.Source) string {
	if s == auth.SourceKeyring {
		return "the system keyring"
	}
	return "a plaintext file"
}

// loginInstructions is the headless path. It names the URL to open and the
// exact command that finishes the job, so an agent or a provisioning script
// can relay both without a human guessing.
//
// It returns the whole answer as one error rather than printing a payload and
// then failing separately: two objects on stdout is not something a caller can
// parse, and the instructions and the exit code describe the same fact.
func loginInstructions() error {
	// The environment variable comes first deliberately. A key passed as
	// --key is visible in ps and in shell history, and this path is taken
	// precisely by CI jobs and agents, where that exposure matters most.
	return clierr.ActionRequired(clierr.CodeNotAuthenticated,
		"no key was supplied and this is not an interactive terminal",
		"open "+dashboardKeysURL+" and create a key with Read analytics",
		"export "+auth.EnvVar+"=stbl_...",
		"or statable auth login --key stbl_...")
}

func promptForKey(rt *Runtime) (string, error) {
	rt.Out.Printf("Create a key at %s (Read analytics is enough).\n", dashboardKeysURL)
	fmt.Fprint(rt.Out.Human(), "Paste your API key: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(rt.Out.Human())
	if err != nil {
		return "", clierr.Wrap(err, "PROMPT", "could not read the key from the terminal")
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", clierr.ActionRequired("EMPTY_KEY", "no key was entered",
			"statable auth login --key stbl_...")
	}
	return key, nil
}

// verifyKey proves the key works before it is stored, so a typo is caught now
// rather than on the next command. Returns the number of readable sites.
func verifyKey(cmd *cobra.Command, rt *Runtime, key string) (int, error) {
	client := api.New(rt.baseURL, key, UserAgent())
	resp, err := client.Get(cmd.Context(), "/sites", nil)
	// --verbose promises the request id of every call, and login's own check
	// was the one call it never reported. A key that is rejected here is
	// exactly when a support-quotable id is worth having.
	rt.traceResponse(resp)
	if err != nil {
		return 0, err
	}
	var payload api.SitesResponse
	if err := api.DecodeInto(resp, &payload); err != nil {
		return 0, err
	}
	return len(payload.Sites), nil
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which key is in use and where it came from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			resolved := rt.Key(cmd.Context())

			if !resolved.Found() {
				if rerr := rt.Out.EmitRecord(output.Record{
					{Name: "authenticated", Value: false, Human: "no"},
					{Name: "source", Value: string(auth.SourceNone), Human: "no key is configured"},
					{Name: "next_steps", Value: []string{
						"export " + auth.EnvVar + "=stbl_...",
						"statable auth login",
					}},
				}); rerr != nil {
					return rerr
				}
				// Not being signed in is not a successful outcome to chain on.
				// `statable auth status && deploy` has to stop here, the way
				// `gh auth status` does; the report is still printed in full.
				return clierr.Silent(clierr.ExitActionRequired)
			}

			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := client.Get(cmd.Context(), "/sites", nil)
			rt.traceResponse(resp)

			rec := output.Record{
				{Name: "authenticated", Value: err == nil,
					Human: map[bool]string{true: "yes", false: "no"}[err == nil]},
				{Name: "source", Value: string(resolved.Source), Human: resolved.Describe()},
				{Name: "key", Value: auth.Redact(resolved.Key)},
			}
			if resolved.Path != "" {
				rec = append(rec, output.Field{Name: "path", Value: resolved.Path, OmitHuman: true})
			}
			if resp != nil && resp.Rate.Known() {
				rec = append(rec,
					output.Field{Name: "rate_limit", Value: resp.Rate.Limit, OmitHuman: true},
					output.Field{Name: "rate_remaining", Value: resp.Rate.Remaining,
						Human: fmt.Sprintf("%d of %d left this hour", resp.Rate.Remaining, resp.Rate.Limit)},
				)
			}
			if err != nil {
				// The request id and the next steps have to survive into the
				// record. This is the command a user runs when support asks
				// for the id, and clierr.Silent below suppresses the renderer
				// that would otherwise have printed them.
				e := clierr.From(err)
				rec = append(rec,
					output.Field{Name: "code", Value: e.Code()},
					output.Field{Name: "error", Value: e.Envelope.Error},
				)
				if e.Envelope.RequestID != "" {
					rec = append(rec, output.Field{
						Name: "request_id", Value: e.Envelope.RequestID})
				}
				if len(e.Envelope.NextSteps) > 0 {
					rec = append(rec, output.Field{
						Name: "next_steps", Value: e.Envelope.NextSteps})
				}
			}
			if rerr := rt.Out.EmitRecord(rec); rerr != nil {
				return rerr
			}

			// The exit code must not depend on the output format: the same
			// condition has to mean the same thing to a person and a script.
			if err != nil {
				return clierr.Silent(clierr.From(err).ExitCode())
			}
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			resolved := rt.Key(cmd.Context())
			removed := rt.Store.Delete(cmd.Context())

			names := make([]string, 0, len(removed))
			for _, src := range removed {
				names = append(names, string(src))
			}

			human := "nothing stored to remove"
			if len(names) > 0 {
				human = "removed from " + strings.Join(names, ", ")
			}
			if rerr := rt.Out.EmitRecord(output.Record{
				{Name: "removed", Value: names, Human: human},
			}); rerr != nil {
				return rerr
			}

			// An environment variable is not ours to unset, so say so rather
			// than leaving the user to wonder why they are still signed in.
			// Still being authenticated after logging out is exactly the class
			// --quiet must not be able to hide, so it is a warning.
			if resolved.Source == auth.SourceEnv {
				rt.Out.Warn("%s is still set in this shell; unset it to finish signing out", auth.EnvVar)
			}
			return nil
		},
	}
}
