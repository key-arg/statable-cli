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

// goalOperators are what the server accepts for a path goal. The letters are
// the API's, and nobody remembers them, so the words are accepted too.
var goalOperators = map[string]string{
	"equals": "e", "e": "e",
	"begins": "b", "b": "b",
	"contains": "c", "c": "c",
}

// goalFlags are shared by create and edit: the same body, the same rules.
type goalFlags struct {
	name     string
	path     string
	operator string
	event    string
	scroll   int
}

func (g *goalFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&g.name, "name", "", "what the goal is called")
	f.StringVar(&g.path, "path", "", "a URL path to match")
	f.StringVar(&g.operator, "operator", "equals", "how to match the path: equals, begins or contains")
	f.StringVar(&g.event, "event", "", "a custom event name to match instead of a path")
	f.IntVar(&g.scroll, "scroll", 0, "percent scrolled, 1 to 100")
}

// body builds the request, refusing the combinations the server refuses.
func (g *goalFlags) body(cmd *cobra.Command) (api.CreateGoalRequest, error) {
	var b api.CreateGoalRequest
	b.Name = strings.TrimSpace(g.name)
	if b.Name == "" {
		return b, clierr.Fail("INVALID_GOAL", "a goal needs a --name").
			WithExit(clierr.ExitUsage)
	}

	// Exactly one kind. The server fills one field and leaves the rest absent,
	// and sending two is a goal that cannot say what it matches.
	kinds := 0
	if cmd.Flags().Changed("path") {
		kinds++
	}
	if cmd.Flags().Changed("event") {
		kinds++
	}
	if cmd.Flags().Changed("scroll") {
		kinds++
	}
	switch kinds {
	case 0:
		return b, clierr.Fail("INVALID_GOAL",
			"a goal matches one thing: give --path, --event or --scroll").
			WithExit(clierr.ExitUsage)
	case 1:
	default:
		return b, clierr.Fail("INVALID_GOAL",
			"a goal matches one thing; --path, --event and --scroll are alternatives").
			WithExit(clierr.ExitUsage)
	}

	switch {
	case cmd.Flags().Changed("path"):
		op, ok := goalOperators[strings.ToLower(strings.TrimSpace(g.operator))]
		if !ok {
			return b, clierr.Failf(clierr.CodeInvalidFormat,
				"unknown operator %q; use equals, begins or contains", g.operator).
				WithExit(clierr.ExitUsage)
		}
		p := g.path
		b.Path, b.Operator = &p, op
	case cmd.Flags().Changed("event"):
		e := strings.TrimSpace(g.event)
		if e == "" {
			return b, clierr.Fail("INVALID_GOAL", "--event needs an event name").
				WithExit(clierr.ExitUsage)
		}
		b.EventName = &e
	default:
		if g.scroll < 1 || g.scroll > 100 {
			return b, clierr.Failf("INVALID_GOAL",
				"--scroll is a percentage from 1 to 100, got %d", g.scroll).
				WithExit(clierr.ExitUsage)
		}
		n := g.scroll
		b.ScrollDepth = &n
	}
	return b, nil
}

func newGoalsCreateCmd() *cobra.Command {
	var g goalFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a goal",
		Long: "Define a conversion goal.\n\n" +
			"A goal matches one of three things, and only one: a page, a custom\n" +
			"event, or a scroll depth.\n\n" +
			"Examples:\n" +
			"  statable goals create --name Signup --event Signup\n" +
			"  statable goals create --name Pricing --path /pricing\n" +
			"  statable goals create --name Blog --path /blog --operator begins\n" +
			"  statable goals create --name 'Read to end' --scroll 90",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			body, err := g.body(cmd)
			if err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			var out api.Goal
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				resp, rerr := client.Post(cmd.Context(),
					fmt.Sprintf("/sites/%d/goals", site.SiteID), body)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "creating a goal")
				}
				return api.DecodeInto(resp, &out)
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "id", Value: out.ID,
					Human: fmt.Sprintf("created goal %d: %s matches %s %s",
						out.ID, out.Name, out.Kind(), out.Target())},
				{Name: "name", Value: out.Name, OmitHuman: true},
				{Name: "kind", Value: out.Kind(), OmitHuman: true},
				{Name: "target", Value: out.Target(), OmitHuman: true},
			})
		},
	}
	g.register(cmd)
	return cmd
}

func newGoalsEditCmd() *cobra.Command {
	var g goalFlags
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Replace a goal",
		Long: "Replace a goal with a new definition.\n\n" +
			"This is a replacement, not a patch: the goal becomes exactly what the\n" +
			"flags say, and anything not given is cleared. Give every flag the goal\n" +
			"should end up with, not only the one you are changing.\n\n" +
			"Examples:\n" +
			"  statable goals edit 3 --name Signup --event SignupCompleted",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil || id <= 0 {
				return clierr.Failf("INVALID_GOAL_ID",
					"%q is not a goal id; run `statable goals` to see them", args[0]).
					WithExit(clierr.ExitUsage)
			}
			body, err := g.body(cmd)
			if err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				resp, rerr := client.Put(cmd.Context(),
					fmt.Sprintf("/sites/%d/goals/%d", site.SiteID, id), body)
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "changing a goal")
				}
				return nil
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "ok", Human: fmt.Sprintf("goal %d replaced", id)},
				{Name: "id", Value: id, OmitHuman: true},
			})
		},
	}
	g.register(cmd)
	return cmd
}

func newGoalsDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"remove"},
		Short:   "Delete a goal",
		Long: "Delete a goal definition.\n\n" +
			"The events the goal matched are not deleted -- they were never stored\n" +
			"as conversions, only counted as them at query time. Reports that used\n" +
			"this goal stop being possible; the underlying traffic is untouched.\n\n" +
			"Examples:\n" +
			"  statable goals delete 3\n" +
			"  statable goals delete 3 --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil || id <= 0 {
				return clierr.Failf("INVALID_GOAL_ID",
					"%q is not a goal id; run `statable goals` to see them", args[0]).
					WithExit(clierr.ExitUsage)
			}
			if err := rt.confirm(yes, fmt.Sprintf("goal %d", id)); err != nil {
				return err
			}
			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			err = rt.withSite(cmd.Context(), func(site api.Site) error {
				resp, rerr := client.Delete(cmd.Context(),
					fmt.Sprintf("/sites/%d/goals/%d", site.SiteID, id))
				rt.traceResponse(resp)
				if rerr != nil {
					return explainWriteDisabled(rerr, "deleting a goal")
				}
				return nil
			})
			if err != nil {
				return err
			}
			return rt.Out.EmitRecord(output.Record{
				{Name: "status", Value: "deleted", Human: fmt.Sprintf("goal %d is gone", id)},
				{Name: "id", Value: id, OmitHuman: true},
			})
		},
	}
	registerConfirm(cmd, &yes)
	return cmd
}
