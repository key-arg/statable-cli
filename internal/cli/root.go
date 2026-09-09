// Package cli wires the command tree together.
//
// Everything about how the process was invoked is resolved exactly once here,
// in PersistentPreRunE, and handed to commands as a Runtime. A command must
// never call os.Getenv, never probe a terminal, and never print with fmt to
// os.Stdout directly. That discipline is what keeps machine output clean and
// keeps the CLI from blocking where nobody can answer.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/auth"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/execctx"
	"github.com/key-arg/statable-cli/internal/output"
	"github.com/key-arg/statable-cli/internal/projconf"
)

// Runtime is everything a command needs, resolved once.
type Runtime struct {
	Ctx   execctx.Context
	Out   *output.Writer
	Store *auth.Store
	Proj  projconf.Config

	baseURL string

	// The credential is resolved on first use, not at startup. Resolving it
	// eagerly made every command — including `version` — wait on the system
	// keyring, which can be locked or wedged for up to the full timeout.
	keyOnce  sync.Once
	keyMu    sync.Mutex
	key      auth.Resolved
	resolved bool

	// The site list is fetched at most once per process for the same reason
	// the credential is: a command that needs it twice should not pay twice.
	// sitesFromCache records that the listing came off disk, which is what
	// lets withSite tell a stale entry apart from a genuine refusal.
	sitesMu        sync.Mutex
	sitesLoaded    bool
	sitesFromCache bool
	sitesNoCache   bool
	sites          []api.Site
	flagKey        string
	env            execctx.Lookup

	// stdout and stderr are captured at construction so a failure that
	// happens before Out exists — a malformed command line, for instance —
	// still writes to the streams the caller supplied rather than to the
	// process globals. Without this the CLI is not fully testable.
	stdout io.Writer
	stderr io.Writer
}

// FlagKey is the key given on the command line, or "" if none was. It answers
// "did the user pass --key" without touching the keyring, which resolving the
// credential would do and which is the whole wait the lazy path avoids.
func (r *Runtime) FlagKey() string { return r.flagKey }

// keyResolved reports whether the credential has been looked up yet. It backs
// the test that keeps commands needing no key from touching the keyring, which
// a wall-clock assertion cannot do: the real keyring answers instantly
// everywhere the test runs.
func (r *Runtime) keyResolved() bool {
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	return r.resolved
}

// Key resolves the credential, at most once per process. It takes a context
// so a wedged keyring can be cut short by Ctrl-C rather than holding the
// process for the full timeout.
func (r *Runtime) Key(ctx context.Context) auth.Resolved {
	r.keyOnce.Do(func() {
		k := r.Store.Resolve(ctx, r.flagKey, auth.Lookup(r.env))
		r.keyMu.Lock()
		r.key, r.resolved = k, true
		r.keyMu.Unlock()
	})
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	return r.key
}

// Client returns an API client for the resolved key, or an action_required
// error naming how to log in. Commands must go through this rather than
// building a client themselves, so the "not authenticated" path is identical
// everywhere.
//
// The environment variable is named before the flag: a key on the command
// line is visible in ps and in shell history, and this is exactly the path a
// CI job or an agent takes.
func (r *Runtime) Client(ctx context.Context) (*api.Client, error) {
	k := r.Key(ctx)
	if !k.Found() {
		return nil, clierr.ActionRequired(clierr.CodeNotAuthenticated,
			"no API key is configured",
			fmt.Sprintf("export %s=stbl_...", auth.EnvVar),
			"statable auth login")
	}
	return api.New(r.baseURL, k.Key, UserAgent()), nil
}

type runtimeKey struct{}

// From pulls the Runtime out of a command's context.
func From(ctx context.Context) *Runtime {
	rt, _ := ctx.Value(runtimeKey{}).(*Runtime)
	return rt
}

type globalFlags struct {
	format   string
	json     bool
	noColor  bool
	noInput  bool
	quiet    bool
	verbose  bool
	key      string
	site     string
	insecure bool
}

// noHomeDirName is the last-resort config directory, under the system temp
// directory. It is world-writable territory: anyone on the machine can create
// it first, or plant files in it. Nothing that has to be trusted may be read
// from there, which is why the site cache refuses to use it.
const noHomeDirName = "statable-no-home"

// isUntrustedConfigDir reports whether the config directory is the temp-dir
// fallback rather than a real, private location.
func isUntrustedConfigDir(dir string) bool {
	return filepath.Clean(dir) == filepath.Clean(filepath.Join(os.TempDir(), noHomeDirName))
}

// configDir resolves the XDG config location for our own state.
func configDir(env execctx.Lookup) string {
	if v, ok := env("STATABLE_CONFIG_DIR"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if v, ok := env("XDG_CONFIG_HOME"); ok && strings.TrimSpace(v) != "" {
		return filepath.Join(v, "statable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// A relative path here means credentials and the default site land in
		// whatever directory the command was run from: different state per
		// directory, and a config file that can end up committed. The fall
		// back is the system temp directory, and it is announced, so the
		// operator can point STATABLE_CONFIG_DIR somewhere real.
		return filepath.Join(os.TempDir(), noHomeDirName)
	}
	return filepath.Join(home, ".config", "statable")
}

// baseURL resolves the API address.
//
// It is read from the environment only, never from the project file. A
// repository-committed file that could redirect the API would send the
// caller's key to an address chosen by whoever opened the pull request.
func baseURL(env execctx.Lookup) string {
	if v, ok := env("STATABLE_API_URL"); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return api.DefaultBaseURL
}

// NewRoot builds the command tree. stdout and stderr are injected so the
// whole CLI can be exercised in tests without touching the real streams.
func NewRoot(stdout, stderr *os.File) (*cobra.Command, *Runtime) {
	var g globalFlags
	rt := &Runtime{stdout: stdout, stderr: stderr}

	root := &cobra.Command{
		Use:   "statable",
		Short: "Statable analytics from the command line",
		Long: "Read your Statable analytics over HTTP, from a terminal, a script, or CI.\n\n" +
			"Every command prints and exits. Add --json to any of them for machine-readable\n" +
			"output on stdout; notes and warnings always go to stderr, so a pipeline stays clean.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// A bare `statable` prints help, because an empty invocation is a
		// question rather than a mistake. In a machine format there is no
		// help to give: prose on stdout would break the caller's parser, so
		// it is a usage error naming where the human version lives.
		RunE: func(cmd *cobra.Command, args []string) error {
			return helpOrUsage(cmd)
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.format, "format", "", "output format: human, json or csv")
	pf.BoolVar(&g.json, "json", false, "shorthand for --format json")
	pf.BoolVar(&g.noColor, "no-color", false, "disable colour (NO_COLOR is honoured too)")
	pf.BoolVar(&g.noInput, "no-input", false, "never prompt; fail instead")
	pf.BoolVarP(&g.quiet, "quiet", "q", false, "suppress informational notes on stderr")
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "show request ids and underlying causes")
	pf.StringVar(&g.key, "key", "", "API key to use for this invocation")
	pf.StringVar(&g.site, "site", "", "site id or domain")
	// --insecure-storage is NOT persistent. It changes only where a key is
	// written, which only `auth login` does, and a root flag put it in the
	// help of every command it has no effect on. It is bound to the same
	// variable, which the store reads after flag parsing and before RunE.

	// A malformed flag is a usage error, not a failure of the operation:
	// nothing was sent anywhere. Cobra reports it through this hook, which is
	// the only typed way to catch it.
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return clierr.Wrap(err, "USAGE", err.Error()).WithExit(clierr.ExitUsage)
	})

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		env := execctx.Lookup(os.LookupEnv)

		format := execctx.Format(strings.ToLower(strings.TrimSpace(g.format)))
		if g.json && g.format != "" && format != execctx.FormatJSON {
			return clierr.Fail(clierr.CodeInvalidFormat,
				fmt.Sprintf("--json and --format %s ask for different things", g.format),
				"statable --json",
				"statable --format "+g.format).WithExit(clierr.ExitUsage)
		}
		if g.format != "" && !format.Valid() {
			return clierr.Fail(clierr.CodeInvalidFormat,
				fmt.Sprintf("unknown format %q", g.format),
				"statable --format human",
				"statable --format json",
				"statable --format csv").WithExit(clierr.ExitUsage)
		}

		ctx := execctx.Detect(execctx.Probe{
			Env:       env,
			StdinTTY:  term.IsTerminal(int(os.Stdin.Fd())),
			StdoutTTY: term.IsTerminal(int(stdout.Fd())),
			StderrTTY: term.IsTerminal(int(stderr.Fd())),
			GOOS:      runtime.GOOS,
			Format:    format,
			JSONFlag:  g.json,
			NoColor:   g.noColor,
			NoInput:   g.noInput,
			Verbose:   g.verbose,
			Quiet:     g.quiet,
		})

		rt.Ctx = ctx
		rt.Out = output.New(ctx, stdout, stderr)
		if ctx.BadEnvFormat != "" {
			rt.Out.Warn("STATABLE_FORMAT=%q is not a format this understands; using human output",
				ctx.BadEnvFormat)
		}
		rt.baseURL = baseURL(env)
		rt.Store = &auth.Store{
			Keyring:   auth.SystemKeyring{},
			ConfigDir: configDir(env),
			Insecure:  g.insecure,
		}
		if _, ok := env("STATABLE_CONFIG_DIR"); !ok {
			if _, ok := env("XDG_CONFIG_HOME"); !ok {
				if _, err := os.UserHomeDir(); err != nil {
					rt.Out.Warn("no home directory; using %s. Set STATABLE_CONFIG_DIR to keep settings somewhere durable",
						rt.Store.ConfigDir)
				}
			}
		}
		rt.flagKey = g.key
		rt.env = env

		wd, err := os.Getwd()
		if err == nil {
			pc, perr := projconf.LoadFrom(wd)
			if perr != nil {
				// A config file that does not parse must say so. Silently
				// contributing nothing leaves the user to wonder for an hour
				// why their setting has no effect.
				if path, ok := projconf.Find(wd); ok {
					rt.Out.Warn("%s: could not be parsed, ignoring it entirely: %v", path, perr)
				}
			} else {
				rt.Proj = pc
			}
		}
		// Refused keys are reported rather than dropped: a setting that
		// silently does nothing is worse than one that is rejected out loud.
		for _, k := range rt.Proj.Ignored {
			rt.Out.Warn("%s: ignoring %q; a project file may only set site and period",
				rt.Proj.Path, k)
		}

		if g.site != "" {
			rt.Proj.Site = g.site
		}

		cmd.SetContext(context.WithValue(cmd.Context(), runtimeKey{}, rt))
		return nil
	}

	// Without this cobra writes help and its own errors to the process
	// globals, bypassing the writer that enforces the machine-output rules.
	root.SetOut(stdout)
	root.SetErr(stderr)

	// Help is prose. In a machine format it must not reach stdout, so it goes
	// to stderr instead of corrupting a payload a caller is parsing.
	baseHelp := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if g.json || execctx.Format(strings.ToLower(g.format)).Machine() {
			c.SetOut(stderr)
		}
		baseHelp(c, args)
	})

	root.AddCommand(
		newAuthCmd(&g.insecure),
		newSitesCmd(),
		newNowCmd(),
		newStatsCmd(),
		newSeriesCmd(),
		newTopCmd(),
		newPropsCmd(),
		newFunnelsCmd(),
		newFunnelCmd(),
		newSubscriptionCmd(),
		newQueryCmd(),
		newCheckCmd(),
		newAPICmd(),
		newVersionCmd(),
	)

	registerCompletions(root)

	return root, rt
}

// traceResponse writes the per-call detail --verbose promises: the request id
// and what the call cost against the hourly budget.
//
// Before this the flag appended an underlying cause on failure and did nothing
// else, so it had no effect at all on a successful call or in a machine
// format, while its help text advertised request ids.
func (r *Runtime) traceResponse(resp *api.Response) {
	if !r.Ctx.Verbose || resp == nil || r.Out == nil {
		return
	}
	// Written through Stderr rather than Note: --verbose is an explicit
	// request for this, and --quiet silencing something the user just asked
	// for makes the two flags cancel out with no explanation.
	if resp.RequestID != "" {
		fmt.Fprintf(r.Out.Stderr(), "request id: %s\n", resp.RequestID)
	}
	if resp.Rate.Known() {
		fmt.Fprintf(r.Out.Stderr(), "rate budget: %d of %d left this hour\n",
			resp.Rate.Remaining, resp.Rate.Limit)
	}
}
