package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/output"
)

// The server's own bounds, from funnels.go.
const (
	funnelMinSteps = 2
	funnelMaxSteps = 8
)

// parseFunnelStep reads one --step. A funnel step has a kind and a value, and
// the shorthand keeps those two things in one argument:
//
//	page:/pricing        a page, matched exactly
//	page:/blog:begins    a page, matched by prefix
//	event:Signup         a custom event
//	goal:9               an existing goal by id
//	scroll:90            a scroll depth
//	entry:/landing       the session's entry page
//	exit:/checkout       the session's exit page
func parseFunnelStep(s string) (api.FunnelStep, error) {
	kind, rest, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || strings.TrimSpace(rest) == "" {
		return api.FunnelStep{}, clierr.Failf("INVALID_STEP",
			"step %q needs a kind and a value, like page:/pricing or event:Signup", s).
			WithExit(clierr.ExitUsage)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))

	switch kind {
	case "page", "entry", "exit", "entry_page", "exit_page":
		// The operator is an optional suffix, so it is taken from the END and
		// only when it is one we know. Cutting at the first colon instead made
		// a path containing one -- /a:b -- impossible to express, and silently
		// mangled it when the tail happened to look like an operator.
		value, op := rest, "e"
		if i := strings.LastIndex(rest, ":"); i >= 0 {
			if o, known := goalOperators[strings.ToLower(rest[i+1:])]; known {
				value, op = rest[:i], o
			}
		}
		if value == "" {
			return api.FunnelStep{}, clierr.Failf("INVALID_STEP",
				"step %q has no path", s).WithExit(clierr.ExitUsage)
		}
		k := map[string]string{
			"page": "page", "entry": "entry_page", "exit": "exit_page",
			"entry_page": "entry_page", "exit_page": "exit_page",
		}[kind]
		return api.FunnelStep{Kind: k, Path: &value, Operator: &op}, nil

	case "event":
		e := rest
		return api.FunnelStep{Kind: "event", Event: &e}, nil

	case "goal":
		id, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil || id <= 0 {
			return api.FunnelStep{}, clierr.Failf("INVALID_STEP",
				"goal step %q needs a goal id; run `statable goals` to see them", s).
				WithExit(clierr.ExitUsage)
		}
		return api.FunnelStep{Kind: "goal", GoalID: &id}, nil

	case "scroll":
		// 0 is a real depth on the server's side -- it means the page was
		// reached without scrolling -- so the floor is 0, not 1.
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || n < 0 || n > 100 {
			return api.FunnelStep{}, clierr.Failf("INVALID_STEP",
				"scroll step %q needs a percentage from 0 to 100", s).
				WithExit(clierr.ExitUsage)
		}
		return api.FunnelStep{Kind: "scroll", Threshold: &n}, nil
	}
	return api.FunnelStep{}, clierr.Failf("INVALID_STEP",
		"unknown step kind %q; use page, event, goal, scroll, entry or exit", kind).
		WithExit(clierr.ExitUsage)
}

type funnelFlags struct {
	name   string
	steps  []string
	scope  string
	strict bool
	raw    string
}

func (f *funnelFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "what the funnel is called")
	fl.StringArrayVar(&f.steps, "step", nil,
		"a step, in order; repeat. page:/pricing, event:Signup, goal:9, scroll:90, entry:/x, exit:/y")
	fl.StringVar(&f.scope, "scope", "", "visitor or session")
	fl.BoolVar(&f.strict, "strict-order", false, "steps must happen in the order given")
	fl.StringVar(&f.raw, "steps-json", "", "the steps as raw JSON, for shapes the shorthand cannot express")
}

func (f *funnelFlags) body() (api.CreateFunnelRequest, error) {
	var b api.CreateFunnelRequest
	b.Name = strings.TrimSpace(f.name)
	if b.Name == "" {
		return b, clierr.Fail("INVALID_FUNNEL", "a funnel needs a --name").
			WithExit(clierr.ExitUsage)
	}
	b.Scope, b.StrictOrder = strings.TrimSpace(f.scope), f.strict

	if f.raw != "" {
		if len(f.steps) > 0 {
			return b, clierr.Fail("INVALID_FUNNEL",
				"give steps either as --step or as --steps-json, not both").
				WithExit(clierr.ExitUsage)
		}
		if err := json.Unmarshal([]byte(f.raw), &b.Steps); err != nil {
			return b, clierr.Wrap(err, "INVALID_FUNNEL",
				"--steps-json is not a JSON array of steps").WithExit(clierr.ExitUsage)
		}
	} else {
		for _, s := range f.steps {
			step, err := parseFunnelStep(s)
			if err != nil {
				return b, err
			}
			b.Steps = append(b.Steps, step)
		}
	}
	// funnels.go: funnelMinSteps = 2, funnelMaxSteps = 8. Both ends checked
	// here so a ninth step is refused by name rather than by a 400 that does
	// not say what the limit is.
	if len(b.Steps) < funnelMinSteps || len(b.Steps) > funnelMaxSteps {
		return b, clierr.Failf("INVALID_FUNNEL",
			"a funnel has between %d and %d steps, got %d",
			funnelMinSteps, funnelMaxSteps, len(b.Steps)).
			WithExit(clierr.ExitUsage)
	}
	if b.Scope != "" && b.Scope != "visitor" && b.Scope != "session" {
		return b, clierr.Failf(clierr.CodeInvalidFormat,
			"unknown scope %q; a funnel counts visitors or sessions", b.Scope).
			WithExit(clierr.ExitUsage)
	}
	return b, nil
}

func newFunnelsCreateCmd() *cobra.Command {
	var f funnelFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a funnel",
		Long: "Define a funnel as an ordered list of steps.\n\n" +
			"Steps are given in order and need at least two of them. Each is a kind\n" +
			"and a value:\n\n" +
			"  page:/pricing        a page, matched exactly\n" +
			"  page:/blog:begins    a page, matched by prefix\n" +
			"  event:Signup         a custom event\n" +
			"  goal:9               an existing goal, by id\n" +
			"  scroll:90            a scroll depth\n" +
			"  entry:/landing       the session's entry page\n" +
			"  exit:/checkout       the session's exit page\n\n" +
			"Examples:\n" +
			"  statable funnels create --name Signup --step page:/pricing --step event:Signup\n" +
			"  statable funnels create --name Checkout --step entry:/shop --step goal:9 --strict-order",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			body, err := f.body()
			if err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var out api.Funnel
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				resp, rerr := client.Post(cmd.Context(),
					fmt.Sprintf("/sites/%d/funnels", site.SiteID), body)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "creating a funnel")
				}
				return api.DecodeInto(resp, &out)
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "id", Value: out.ID,
					Human: fmt.Sprintf("created funnel %d: %s, %d steps",
						out.ID, out.Name, len(out.Steps))},
				{Name: "name", Value: out.Name, OmitHuman: true},
				{Name: "steps", Value: len(out.Steps), OmitHuman: true},
			})
		},
	}
	f.register(cmd)
	return cmd
}

func newFunnelsEditCmd() *cobra.Command {
	var f funnelFlags
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Replace a funnel",
		Long: "Replace a funnel with a new definition.\n\n" +
			"A replacement, not a patch: the funnel becomes exactly what the flags\n" +
			"say. Give every step it should end up with.\n\n" +
			"Examples:\n" +
			"  statable funnels edit 45 --name Signup --step page:/pricing --step event:Signup",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := funnelIDArg(args[0])
			if err != nil {
				return err
			}
			body, err := f.body()
			if err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				resp, rerr := client.Put(cmd.Context(),
					fmt.Sprintf("/sites/%d/funnels/%d", site.SiteID, id), body)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "changing a funnel")
				}
				return nil
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "ok", Human: fmt.Sprintf("funnel %d replaced", id)},
				{Name: "id", Value: id, OmitHuman: true},
			})
		},
	}
	f.register(cmd)
	return cmd
}

func newFunnelsDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"remove"},
		Short:   "Delete a funnel",
		Long: "Delete a funnel definition. The traffic it described is untouched:\n" +
			"a funnel is computed at query time, not stored.\n\n" +
			"Examples:\n" +
			"  statable funnels delete 45 --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := funnelIDArg(args[0])
			if err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			// The site is resolved BEFORE the question, and the question names
			// it. Asking "delete funnel 3?" and then working out which site that
			// means is how the wrong one gets deleted.
			//
			// withSite is deliberately not used here. Its repair path retries
			// the call against a freshly resolved site, which is right for a
			// read and wrong for a delete: the retry would remove funnel %d on a
			// different site from the one the user agreed to.
			if _, rerr := rt.refreshSites(cmd.Context()); rerr != nil {
				return rerr
			}
			site, err := rt.resolveSite(cmd.Context())
			if err != nil {
				return err
			}
			if err := rt.confirm(yes, fmt.Sprintf("funnel %d on %s", id, site.Name)); err != nil {
				return err
			}
			resp, err := client.Delete(cmd.Context(),
				fmt.Sprintf("/sites/%d/funnels/%d", site.SiteID, id))
			rt.traceResponse(resp)
			if err != nil {
				return explainWriteDisabled(err, "deleting a funnel")
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "deleted", Human: fmt.Sprintf("funnel %d is gone", id)},
				{Name: "id", Value: id, OmitHuman: true},
			})
		},
	}
	registerConfirm(cmd, &yes)
	return cmd
}

func funnelIDArg(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, clierr.Failf("INVALID_FUNNEL_ID",
			"%q is not a funnel id; run `statable funnels` to see them", s).
			WithExit(clierr.ExitUsage)
	}
	return id, nil
}
