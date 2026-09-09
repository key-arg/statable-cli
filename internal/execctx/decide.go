package execctx

// Every decision the CLI makes about interactivity lives here as a pure
// function of Context. Nothing in this file reads the environment, the
// filesystem, or a global. That is what makes the truth table in the tests
// exhaustive rather than illustrative.

// HumanOutput reports whether prose, tables, colour and hints may be written
// to stdout at all. When false the human writer is replaced by io.Discard, so
// a stray fmt.Println cannot corrupt machine output.
func (c Context) HumanOutput() bool { return !c.Format.Machine() }

// UseColor reports whether ANSI styling should be emitted.
func (c Context) UseColor() bool {
	if c.NoColor {
		return false
	}
	if c.ForceColor {
		return true
	}
	return c.StdoutTTY && c.HumanOutput()
}

// CanPrompt reports whether it is safe to ask the user a question and block
// on the answer. A prompt needs a human on both ends: something to read the
// question and something to type into.
func (c Context) CanPrompt() bool {
	if c.NoInput || c.CI || c.AgentHarness {
		return false
	}
	if !c.HumanOutput() {
		return false
	}
	return c.StdinTTY && c.StdoutTTY
}

// ShowProgress reports whether spinners and progress bars may be drawn.
// They are chrome: never in machine output, never when stderr is redirected.
func (c Context) ShowProgress() bool {
	return c.HumanOutput() && c.StderrTTY && !c.Quiet && !c.CI
}

// LoginMode selects how `auth login` should behave.
type LoginMode int

const (
	// LoginInteractive prompts for the key and reads it from the terminal
	// without echoing.
	LoginInteractive LoginMode = iota
	// LoginNonInteractive prints a machine-readable payload naming where to
	// create a key and how to finish, then exits without blocking. This is
	// what makes the CLI usable inside an agent loop or a provisioning script.
	LoginNonInteractive
)

func (c Context) LoginMode() LoginMode {
	if c.CanPrompt() {
		return LoginInteractive
	}
	return LoginNonInteractive
}

// AuthGate decides what a command that needs credentials should do when it
// has none.
type AuthGate int

const (
	// AuthPrompt: ask for a key inline and continue.
	AuthPrompt AuthGate = iota
	// AuthFailFast: emit a structured NOT_AUTHENTICATED error and exit.
	// Never open a browser nobody can see, never block on input nobody can
	// type. This is the single most important decision in this file.
	AuthFailFast
)

func (c Context) AuthGate() AuthGate {
	if c.CanPrompt() {
		return AuthPrompt
	}
	return AuthFailFast
}

// OpenBrowser reports whether a URL may be launched rather than printed.
// Agents and CI get the URL as text: a relayed URL survives a transcript,
// a launched browser does not exist.
func (c Context) OpenBrowser() bool {
	if c.CI || c.AgentHarness || c.NoInput {
		return false
	}
	if !c.HumanOutput() {
		return false
	}
	return c.BrowserReachable && c.StdoutTTY
}
