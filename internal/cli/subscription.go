package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

func newSubscriptionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "subscription",
		Short: "The plan state of the key's owner",
		Long: "The subscription state of whoever owns the key.\n\n" +
			"It takes no site: the answer is about the account. An account holding\n" +
			"only hobby sites has no subscription and answers status \"none\" with\n" +
			"only_hobby true, which means the free plan, not a missing account.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			// No site is resolved, so this command never lists sites. That is
			// the whole point of the endpoint and worth not undoing here.
			resp, err := client.Get(cmd.Context(), "/subscription", nil)
			rt.traceResponse(resp)
			if err != nil {
				return err
			}
			var s api.Subscription
			if err := api.DecodeInto(resp, &s); err != nil {
				return err
			}
			// Status is the one field this endpoint always sets. Without it
			// the human line is a blank line at exit 0, which reads as
			// success with no answer — the same failure `stats` refuses when
			// none of the requested metrics come back.
			if strings.TrimSpace(s.Status) == "" {
				return clierr.Fail(clierr.CodeServer,
					"the server returned a subscription with no status")
			}
			rec := output.Record{
				{Name: "status", Value: s.Status, Human: describePlan(s)},
				{Name: "is_trial", Value: s.IsTrial, OmitHuman: true},
				{Name: "only_hobby", Value: s.OnlyHobby, OmitHuman: true},
			}
			// Absent is not empty. The API omits ends_at when the date is
			// unknown, and an empty string would tell a caller the value is
			// known and blank. null says what is true.
			var ends any
			if s.EndsAt != nil {
				ends = *s.EndsAt
			}
			rec = append(rec, output.Field{Name: "ends_at", Value: ends, OmitHuman: true})
			return rt.Out.EmitRecord(rec)
		},
	}
}

// planWord renders a status for a person. The machine field keeps the raw
// value, which is what the documentation names and what a script matches on;
// this only affects the sentence a person reads.
func planWord(status string) string {
	switch status {
	case "past_due":
		return "payment overdue"
	case "trial_expired":
		return "trial ended"
	case "expired":
		return "expired"
	}
	return status
}

// describePlan is the one line this endpoint exists to render.
func describePlan(s api.Subscription) string {
	if s.Status == "none" {
		if s.OnlyHobby {
			return "no subscription: this account is on the free hobby plan"
		}
		return "no subscription"
	}
	line := planWord(s.Status)
	if s.IsTrial {
		line = "trial"
	}
	if s.EndsAt == nil {
		return line
	}
	end, err := time.Parse(time.RFC3339, *s.EndsAt)
	if err != nil {
		return fmt.Sprintf("%s until %s", line, *s.EndsAt)
	}
	days := int(time.Until(end).Hours() / 24)
	switch {
	case days < 0:
		return fmt.Sprintf("%s, ended %s", line, end.Format("2006-01-02"))
	case days == 0:
		return fmt.Sprintf("%s, ends today", line)
	case days == 1:
		return fmt.Sprintf("%s, 1 day left", line)
	default:
		return fmt.Sprintf("%s, %d days left", line, days)
	}
}
