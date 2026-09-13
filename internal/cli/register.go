package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/auth"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

func newAuthRegisterCmd(insecure *bool) *cobra.Command {
	var email, code, keyName string
	var accept bool
	var scopes []string

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Create an account and a first key, without a browser",
		Long: "Register by email code and receive a first API key.\n\n" +
			"Two steps, because the code arrives by email. Run it once with --email\n" +
			"to be sent a code, then again with --code to exchange it. On a terminal\n" +
			"the second step is offered right away, so one command does both.\n\n" +
			"The key is stored the same way `auth login` stores one, and the secret\n" +
			"is shown once.\n\n" +
			"Examples:\n" +
			"  statable auth register --email you@example.com\n" +
			"  statable auth register --email you@example.com --code 123456 --accept-terms\n" +
			"  statable auth register --email you@example.com --code 123456 --accept-terms --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			email = strings.TrimSpace(email)
			if email == "" || !strings.Contains(email, "@") {
				return clierr.Fail("INVALID_EMAIL",
					"--email needs an address the code can be sent to").
					WithExit(clierr.ExitUsage)
			}
			// No key is needed to register, and demanding one would make this
			// command impossible to use for its only purpose.
			client := api.New(rt.baseURL, "", UserAgent())

			if code == "" {
				resp, err := client.Post(cmd.Context(), "/auth/send-otp",
					api.SendOTPRequest{Email: email})
				rt.traceResponse(resp)
				if err != nil {
					return err
				}
				// On a terminal, finish the job rather than making the user
				// retype the whole command with the code.
				if rt.Ctx.CanPrompt() && term.IsTerminal(int(os.Stdin.Fd())) {
					fmt.Fprintf(rt.Out.Human(), "A code went to %s. Paste it here: ", email)
					line, rerr := bufio.NewReader(os.Stdin).ReadString('\n')
					if rerr != nil {
						return sentCodeRecord(rt, email)
					}
					code = strings.TrimSpace(line)
					if code == "" {
						return sentCodeRecord(rt, email)
					}
				} else {
					return sentCodeRecord(rt, email)
				}
			}

			if !accept {
				if rt.Ctx.CanPrompt() && term.IsTerminal(int(os.Stdin.Fd())) {
					fmt.Fprint(rt.Out.Human(), "Accept the terms of service? Type yes: ")
					line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
					accept = strings.TrimSpace(strings.ToLower(line)) == "yes"
				}
			}
			if !accept {
				return clierr.ActionRequired("TERMS_NOT_ACCEPTED",
					"the account cannot be created without accepting the terms",
					"add --accept-terms once you have read https://statable.com/terms")
			}

			body := api.VerifyOTPRequest{
				Email: email, Code: strings.TrimSpace(code),
				AcceptTerms: true, KeyName: keyName,
			}
			for _, s := range scopes {
				if !validScope(strings.TrimSpace(s)) {
					return clierr.Failf(clierr.CodeInvalidFormat,
						"unknown scope %q; the API knows %s", s, strings.Join(api.KeyScopes, ", ")).
						WithExit(clierr.ExitUsage)
				}
				body.Scopes = append(body.Scopes, strings.TrimSpace(s))
			}

			resp, err := client.Post(cmd.Context(), "/auth/verify-otp", body)
			rt.traceResponse(resp)
			if err != nil {
				return err
			}
			var out api.CreatedKey
			if err := api.DecodeInto(resp, &out); err != nil {
				return err
			}
			if out.Token == "" {
				return clierr.Fail(clierr.CodeServer,
					"the account was created but no key came back")
			}

			// Store it, so the next command works without the user doing
			// anything else. A failure here is worth reporting, not swallowing:
			// the key exists either way and they need to keep it.
			src, serr := rt.Store.Save(cmd.Context(), out.Token)
			if serr != nil {
				rt.Out.Warn("the key could not be stored: %v", serr)
			} else if w := rt.Store.InsecureWarning(); w != "" && src == auth.SourceFile {
				rt.Out.Warn("%s", w)
			}
			rt.Out.Warn("this is the only time the secret is shown; store it now")

			return rt.Out.EmitRecord(output.Record{
				{Name: "token", Value: out.Token, Human: out.Token},
				{Name: "email", Value: email, OmitHuman: true},
				{Name: "id", Value: out.ID, OmitHuman: true},
				{Name: "prefix", Value: out.Prefix, OmitHuman: true},
				{Name: "scopes", Value: out.Scopes, OmitHuman: true},
				{Name: "stored", Value: string(src), OmitHuman: true},
			})
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "the address that will own the account")
	cmd.Flags().StringVar(&code, "code", "", "the code from the email")
	cmd.Flags().BoolVar(&accept, "accept-terms", false, "accept the terms of service")
	cmd.Flags().StringVar(&keyName, "key-name", "", "what to call the first key")
	cmd.Flags().StringArrayVar(&scopes, "scope", nil, "permission for the first key; repeat for more")
	_ = insecure
	return cmd
}

// sentCodeRecord is the answer when the code has gone out and there is nobody
// to type it: say what happened and exactly what to run next.
func sentCodeRecord(rt *Runtime, email string) error {
	e := clierr.ActionRequired("CODE_SENT",
		fmt.Sprintf("a code was emailed to %s; run the same command again with it", email),
		fmt.Sprintf("statable auth register --email %s --code <code> --accept-terms", email))
	return e.WithExit(clierr.ExitActionRequired)
}
