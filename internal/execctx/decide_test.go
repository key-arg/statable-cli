package execctx

import (
	"fmt"
	"testing"
)

// envOf builds a Lookup from a map so no test ever reads or mutates the real
// process environment.
func envOf(m map[string]string) Lookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

var allFormats = []Format{FormatHuman, FormatJSON, FormatCSV}

// bits enumerates every combination of the booleans the decisions depend on.
// Eight booleans is 256 rows; combined with three formats that is 768 cases,
// which is small enough to check exhaustively on every run.
type bits struct {
	stdinTTY, stdoutTTY, stderrTTY bool
	ci, agent, noInput, quiet      bool
	browser                        bool
}

func enumerate(fn func(b bits)) {
	for i := 0; i < 256; i++ {
		fn(bits{
			stdinTTY:  i&1 != 0,
			stdoutTTY: i&2 != 0,
			stderrTTY: i&4 != 0,
			ci:        i&8 != 0,
			agent:     i&16 != 0,
			noInput:   i&32 != 0,
			quiet:     i&64 != 0,
			browser:   i&128 != 0,
		})
	}
}

func (b bits) context(f Format) Context {
	return Context{
		Format:           f,
		StdinTTY:         b.stdinTTY,
		StdoutTTY:        b.stdoutTTY,
		StderrTTY:        b.stderrTTY,
		CI:               b.ci,
		AgentHarness:     b.agent,
		BrowserReachable: b.browser,
		NoInput:          b.noInput,
		Quiet:            b.quiet,
	}
}

func (b bits) String() string {
	return fmt.Sprintf("stdin=%v stdout=%v stderr=%v ci=%v agent=%v noinput=%v quiet=%v browser=%v",
		b.stdinTTY, b.stdoutTTY, b.stderrTTY, b.ci, b.agent, b.noInput, b.quiet, b.browser)
}

// TestTruthTable asserts the invariants that must hold for every reachable
// combination of inputs. These are the rules that keep the CLI from hanging in
// CI or corrupting machine output; a regression in any of them is a release
// blocker, not a style issue.
func TestTruthTable(t *testing.T) {
	enumerate(func(b bits) {
		for _, f := range allFormats {
			c := b.context(f)
			name := fmt.Sprintf("format=%s %s", f, b)

			// 1. Machine output is absolute: no prose, no prompts, no
			// progress, no browser, no matter what else is true.
			if f.Machine() {
				if c.HumanOutput() {
					t.Fatalf("%s: HumanOutput must be false for machine format", name)
				}
				if c.CanPrompt() {
					t.Fatalf("%s: CanPrompt must be false for machine format", name)
				}
				if c.ShowProgress() {
					t.Fatalf("%s: ShowProgress must be false for machine format", name)
				}
				if c.OpenBrowser() {
					t.Fatalf("%s: OpenBrowser must be false for machine format", name)
				}
				if c.AuthGate() != AuthFailFast {
					t.Fatalf("%s: AuthGate must fail fast for machine format", name)
				}
				if c.LoginMode() != LoginNonInteractive {
					t.Fatalf("%s: LoginMode must be non-interactive for machine format", name)
				}
			}

			// 2. CI never prompts, never draws progress, never opens a browser.
			if c.CI {
				if c.CanPrompt() {
					t.Fatalf("%s: CanPrompt must be false in CI", name)
				}
				if c.ShowProgress() {
					t.Fatalf("%s: ShowProgress must be false in CI", name)
				}
				if c.OpenBrowser() {
					t.Fatalf("%s: OpenBrowser must be false in CI", name)
				}
			}

			// 3. An agent harness gets text, never a prompt or a browser.
			if c.AgentHarness {
				if c.CanPrompt() {
					t.Fatalf("%s: CanPrompt must be false under an agent harness", name)
				}
				if c.OpenBrowser() {
					t.Fatalf("%s: OpenBrowser must be false under an agent harness", name)
				}
			}

			// 4. --no-input is honoured unconditionally.
			if c.NoInput {
				if c.CanPrompt() {
					t.Fatalf("%s: CanPrompt must be false with --no-input", name)
				}
				if c.OpenBrowser() {
					t.Fatalf("%s: OpenBrowser must be false with --no-input", name)
				}
			}

			// 5. A prompt needs a human on both ends.
			if c.CanPrompt() && !(c.StdinTTY && c.StdoutTTY) {
				t.Fatalf("%s: CanPrompt requires both stdin and stdout to be terminals", name)
			}

			// 6. The auth gate and the login mode are exactly CanPrompt.
			// If these ever diverge, one call site will hang while another
			// fails, which is the bug class this package exists to prevent.
			wantGate := AuthFailFast
			wantLogin := LoginNonInteractive
			if c.CanPrompt() {
				wantGate = AuthPrompt
				wantLogin = LoginInteractive
			}
			if c.AuthGate() != wantGate {
				t.Fatalf("%s: AuthGate disagrees with CanPrompt", name)
			}
			if c.LoginMode() != wantLogin {
				t.Fatalf("%s: LoginMode disagrees with CanPrompt", name)
			}

			// 7. Progress is chrome on stderr and requires a terminal there.
			if c.ShowProgress() && !c.StderrTTY {
				t.Fatalf("%s: ShowProgress requires stderr to be a terminal", name)
			}

			// 8. A browser is only launched when one could plausibly be seen.
			if c.OpenBrowser() && !c.BrowserReachable {
				t.Fatalf("%s: OpenBrowser requires a reachable browser", name)
			}
		}
	})
}

// TestColorPrecedence pins the NO_COLOR / FORCE_COLOR contract. NO_COLOR wins
// over everything, including an explicit FORCE_COLOR, per no-color.org.
func TestColorPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		flag   bool
		stdout bool
		format Format
		want   bool
	}{
		{"tty human, no vars", nil, false, true, FormatHuman, true},
		{"not a tty", nil, false, false, FormatHuman, false},
		{"json format on a tty", nil, false, true, FormatJSON, false},
		{"--no-color", nil, true, true, FormatHuman, false},
		{"NO_COLOR set", map[string]string{"NO_COLOR": "1"}, false, true, FormatHuman, false},
		{"NO_COLOR empty is not set", map[string]string{"NO_COLOR": ""}, false, true, FormatHuman, true},
		{"FORCE_COLOR without a tty", map[string]string{"FORCE_COLOR": "1"}, false, false, FormatHuman, true},
		{"NO_COLOR beats FORCE_COLOR", map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, false, true, FormatHuman, false},
		{"CLICOLOR=0", map[string]string{"CLICOLOR": "0"}, false, true, FormatHuman, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Detect(Probe{
				Env:       envOf(tc.env),
				StdoutTTY: tc.stdout,
				NoColor:   tc.flag,
				Format:    tc.format,
				GOOS:      "linux",
			})
			if got := c.UseColor(); got != tc.want {
				t.Fatalf("UseColor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectCI(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"empty", nil, false},
		{"CI=true", map[string]string{"CI": "true"}, true},
		{"CI=1", map[string]string{"CI": "1"}, true},
		{"CI=false is not CI", map[string]string{"CI": "false"}, false},
		{"CI empty is not CI", map[string]string{"CI": ""}, false},
		{"GitHub Actions", map[string]string{"GITHUB_ACTIONS": "true"}, true},
		{"GitLab", map[string]string{"GITLAB_CI": "true"}, true},
		{"Azure sets an id only", map[string]string{"BUILD_BUILDID": "42"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(Probe{Env: envOf(tc.env)}).CI; got != tc.want {
				t.Fatalf("CI = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectAgent(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"empty", nil, false},
		{"claude code", map[string]string{"CLAUDECODE": "1"}, true},
		{"explicit override", map[string]string{"STATABLE_AGENT": "1"}, true},
		{"override disabled", map[string]string{"STATABLE_AGENT": "0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(Probe{Env: envOf(tc.env)}).AgentHarness; got != tc.want {
				t.Fatalf("AgentHarness = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectBrowser(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		goos string
		want bool
	}{
		{"macOS always", nil, "darwin", true},
		{"bare linux, no display", nil, "linux", false},
		{"linux with X", map[string]string{"DISPLAY": ":0"}, "linux", true},
		{"linux with wayland", map[string]string{"WAYLAND_DISPLAY": "wayland-1"}, "linux", true},
		{"BROWSER overrides", map[string]string{"BROWSER": "firefox"}, "linux", true},
		{"BROWSER empty does not", map[string]string{"BROWSER": "  "}, "linux", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(Probe{Env: envOf(tc.env), GOOS: tc.goos}).BrowserReachable
			if got != tc.want {
				t.Fatalf("BrowserReachable = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestJSONFlagWins guards the shorthand: --json must be exactly --format json.
func TestJSONFlagWins(t *testing.T) {
	c := Detect(Probe{Env: envOf(nil), Format: FormatHuman, JSONFlag: true})
	if c.Format != FormatJSON {
		t.Fatalf("Format = %q, want json", c.Format)
	}
	if c.HumanOutput() {
		t.Fatal("HumanOutput must be false when --json is set")
	}
}

// TestSSHSessionCannotPrompt is the scenario this package was written for: a
// non-interactive remote invocation must fail fast rather than block forever.
func TestSSHSessionCannotPrompt(t *testing.T) {
	c := Detect(Probe{
		Env:       envOf(map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22"}),
		StdinTTY:  false,
		StdoutTTY: false,
		GOOS:      "linux",
	})
	if c.CanPrompt() {
		t.Fatal("a piped remote invocation must not prompt")
	}
	if c.AuthGate() != AuthFailFast {
		t.Fatal("a piped remote invocation must fail fast on missing credentials")
	}
}

// TestZeroFormatIsHuman guards the one moment a zero Context exists: a command
// line so malformed that the format was never resolved. That failure must be
// reported as text on stderr, not as JSON on stdout.
func TestZeroFormatIsHuman(t *testing.T) {
	var zero Context
	if zero.Format.Machine() {
		t.Fatal("the zero format must not be treated as machine readable")
	}
	if !zero.HumanOutput() {
		t.Fatal("the zero context must produce human output")
	}
}

func TestUnrecognisedFormatIsHuman(t *testing.T) {
	if Format("xml").Machine() {
		t.Fatal("an unrecognised format must not be treated as machine readable")
	}
	if Format("xml").Valid() {
		t.Fatal("an unrecognised format must not validate")
	}
}

// TestTruthTableHasPositiveCases. The invariants above are all one-directional
// ("must be false when…"), so an implementation that returned false from every
// decision passed all 768 combinations. Verified: replacing CanPrompt,
// ShowProgress and OpenBrowser with `return false` left the whole suite green.
//
// These cases pin the other half of the table: the conditions under which each
// decision must be true. Without them the package only proves the CLI never
// prompts, not that it ever does.
func TestTruthTableHasPositiveCases(t *testing.T) {
	interactive := Context{
		Format:           FormatHuman,
		StdinTTY:         true,
		StdoutTTY:        true,
		StderrTTY:        true,
		BrowserReachable: true,
	}

	if !interactive.CanPrompt() {
		t.Fatal("a person at a terminal must be promptable")
	}
	if !interactive.ShowProgress() {
		t.Fatal("progress belongs on an interactive terminal")
	}
	if !interactive.OpenBrowser() {
		t.Fatal("a reachable browser on a terminal may be opened")
	}
	if !interactive.HumanOutput() {
		t.Fatal("human format means human output")
	}
	if !interactive.UseColor() {
		t.Fatal("colour belongs on an interactive terminal")
	}
	if interactive.AuthGate() != AuthPrompt {
		t.Fatal("an interactive session asks for a key rather than failing")
	}
	if interactive.LoginMode() != LoginInteractive {
		t.Fatal("an interactive session logs in interactively")
	}

	// Each field is removed one at a time, so every guard is shown to matter
	// on its own rather than by being lumped together.
	for _, tc := range []struct {
		name   string
		mutate func(Context) Context
	}{
		{"no stdin", func(c Context) Context { c.StdinTTY = false; return c }},
		{"no stdout", func(c Context) Context { c.StdoutTTY = false; return c }},
		{"in CI", func(c Context) Context { c.CI = true; return c }},
		{"under an agent", func(c Context) Context { c.AgentHarness = true; return c }},
		{"--no-input", func(c Context) Context { c.NoInput = true; return c }},
		{"json format", func(c Context) Context { c.Format = FormatJSON; return c }},
		{"csv format", func(c Context) Context { c.Format = FormatCSV; return c }},
	} {
		t.Run("cannot prompt "+tc.name, func(t *testing.T) {
			if tc.mutate(interactive).CanPrompt() {
				t.Fatalf("%s must make prompting impossible", tc.name)
			}
		})
	}

	// ShowProgress and OpenBrowser have their own necessary conditions.
	if c := interactive; func() bool { c.StderrTTY = false; return c.ShowProgress() }() {
		t.Fatal("progress needs a terminal on stderr")
	}
	if c := interactive; func() bool { c.Quiet = true; return c.ShowProgress() }() {
		t.Fatal("--quiet must silence progress")
	}
	if c := interactive; func() bool { c.BrowserReachable = false; return c.OpenBrowser() }() {
		t.Fatal("no reachable browser means no browser")
	}
}

// TestDecisionsAreNotAllConstant is a direct guard against the class of
// implementation that satisfies every negative invariant by never returning
// true. It fails loudly if any decision loses its positive branch.
func TestDecisionsAreNotAllConstant(t *testing.T) {
	decisions := map[string]func(Context) bool{
		"HumanOutput":  Context.HumanOutput,
		"UseColor":     Context.UseColor,
		"CanPrompt":    Context.CanPrompt,
		"ShowProgress": Context.ShowProgress,
		"OpenBrowser":  Context.OpenBrowser,
	}
	for name, fn := range decisions {
		sawTrue, sawFalse := false, false
		enumerate(func(b bits) {
			for _, f := range allFormats {
				if fn(b.context(f)) {
					sawTrue = true
				} else {
					sawFalse = true
				}
			}
		})
		if !sawTrue {
			t.Fatalf("%s is never true for any input; it has lost its positive branch", name)
		}
		if !sawFalse {
			t.Fatalf("%s is never false for any input; it has lost its guards", name)
		}
	}
}
