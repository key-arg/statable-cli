// Package execctx resolves, exactly once, everything about how this process
// was invoked: output format, whether the streams are terminals, whether we
// are inside CI or an agent harness, and whether a browser could plausibly be
// opened.
//
// Commands must never re-derive any of this. Re-assembling the same scattered
// booleans at each call site, each combining them slightly differently, is how
// "hung waiting for a prompt in CI" bugs appear. Detect once here, then let
// every decision be a pure function of the resulting Context so the decisions
// can be exhaustively truth-tabled in tests without touching the environment.
package execctx

import "strings"

// Format is the output format selected by --format (or --json).
type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
)

// Valid reports whether f is a format the CLI knows how to write.
func (f Format) Valid() bool {
	switch f {
	case FormatHuman, FormatJSON, FormatCSV:
		return true
	}
	return false
}

// Machine reports whether the format is meant for a program rather than a
// person. Human-facing chrome (spinners, tables, colour, hints) must be
// suppressed structurally whenever this is true.
//
// Only the known machine formats answer true. An unset or unrecognised value
// is treated as human, because the zero Context exists in exactly one moment:
// a command line so malformed that the format was never resolved. Reporting
// that failure as JSON on stdout would be the wrong answer to a question the
// caller never got to ask.
func (f Format) Machine() bool { return f == FormatJSON || f == FormatCSV }

// Lookup mirrors os.LookupEnv so tests can supply an environment without
// mutating the real one.
type Lookup func(string) (string, bool)

// Probe is the raw input to Detect. Everything the Context knows is derived
// from these fields and nothing else.
type Probe struct {
	Env       Lookup
	StdinTTY  bool
	StdoutTTY bool
	StderrTTY bool
	GOOS      string

	// Flags already parsed from the command line. Zero values mean "not set".
	Format   Format
	JSONFlag bool
	NoColor  bool
	NoInput  bool
	Verbose  bool
	Quiet    bool
}

// Context is the resolved answer. It is immutable once built.
type Context struct {
	Format Format

	StdinTTY  bool
	StdoutTTY bool
	StderrTTY bool

	CI               bool
	AgentHarness     bool
	BrowserReachable bool

	// BadEnvFormat holds an unusable STATABLE_FORMAT value so the caller can
	// say the variable was ignored instead of quietly falling back.
	BadEnvFormat string

	NoColor    bool
	ForceColor bool
	NoInput    bool
	Verbose    bool
	Quiet      bool
}

// ciVars are set by the CI systems we care about. Presence is enough; several
// of these are set to an empty string by some runners, so we do not inspect
// the value except for the generic CI var, which Travis historically set to
// "false" on non-CI machines.
var ciVars = []string{
	"BUILDKITE",
	"BUILD_BUILDID", // Azure Pipelines
	"CIRCLECI",
	"CODEBUILD_BUILD_ID",
	"DRONE",
	"GITHUB_ACTIONS",
	"GITLAB_CI",
	"JENKINS_URL",
	"TEAMCITY_VERSION",
	"TF_BUILD",
	"WOODPECKER",
}

// agentVars are set by coding-agent harnesses that run this binary
// non-interactively but can still relay a URL back to a human.
// STATABLE_AGENT is our own explicit override for harnesses we do not know.
var agentVars = []string{
	"CLAUDECODE",
	"CURSOR_AGENT",
	"OPENAI_CODEX",
	"REPLIT_ENVIRONMENT",
	"STATABLE_AGENT",
}

func has(env Lookup, key string) bool {
	_, ok := env(key)
	return ok
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func detectCI(env Lookup) bool {
	if v, ok := env("CI"); ok && truthy(v) {
		return true
	}
	if v, ok := env("CONTINUOUS_INTEGRATION"); ok && truthy(v) {
		return true
	}
	for _, k := range ciVars {
		if has(env, k) {
			return true
		}
	}
	return false
}

func detectAgent(env Lookup) bool {
	for _, k := range agentVars {
		if v, ok := env(k); ok && truthy(v) {
			return true
		}
	}
	return false
}

// detectBrowser reports whether opening a URL could plausibly reach a human.
// A false answer must never be treated as fatal on its own; it only selects a
// flow that prints the URL instead of trying to launch it.
func detectBrowser(env Lookup, goos string) bool {
	if v, ok := env("BROWSER"); ok && strings.TrimSpace(v) != "" {
		return true
	}
	switch goos {
	case "darwin", "windows":
		return true
	}
	// Unix: a display server has to be reachable. An SSH session without
	// forwarding has neither.
	return has(env, "DISPLAY") || has(env, "WAYLAND_DISPLAY")
}

// detectColor applies the NO_COLOR and FORCE_COLOR conventions.
// https://no-color.org: any non-empty value disables colour.
func detectColor(env Lookup, flagNoColor bool) (noColor, forceColor bool) {
	if v, ok := env("FORCE_COLOR"); ok && truthy(v) {
		forceColor = true
	}
	if v, ok := env("CLICOLOR_FORCE"); ok && truthy(v) {
		forceColor = true
	}
	if v, ok := env("NO_COLOR"); ok && strings.TrimSpace(v) != "" {
		return true, false
	}
	if v, ok := env("CLICOLOR"); ok && !truthy(v) {
		return true, false
	}
	if flagNoColor {
		return true, false
	}
	return false, forceColor
}

// Detect resolves the Probe into a Context. It reads no globals.
func Detect(p Probe) Context {
	env := p.Env
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}

	format := p.Format
	if p.JSONFlag {
		format = FormatJSON
	}
	badEnvFormat := ""
	if format == "" {
		if v, ok := env("STATABLE_FORMAT"); ok && strings.TrimSpace(v) != "" {
			if f := Format(strings.ToLower(strings.TrimSpace(v))); f.Valid() {
				format = f
			} else {
				// Falling back to human without a word left the variable
				// looking honoured while it was not.
				badEnvFormat = v
			}
		}
	}
	if format == "" {
		format = FormatHuman
	}

	noColor, forceColor := detectColor(env, p.NoColor)

	noInput := p.NoInput
	if v, ok := env("STATABLE_NO_INPUT"); ok && truthy(v) {
		noInput = true
	}

	return Context{
		Format:           format,
		BadEnvFormat:     badEnvFormat,
		StdinTTY:         p.StdinTTY,
		StdoutTTY:        p.StdoutTTY,
		StderrTTY:        p.StderrTTY,
		CI:               detectCI(env),
		AgentHarness:     detectAgent(env),
		BrowserReachable: detectBrowser(env, p.GOOS),
		NoColor:          noColor,
		ForceColor:       forceColor,
		NoInput:          noInput,
		Verbose:          p.Verbose,
		Quiet:            p.Quiet,
	}
}
