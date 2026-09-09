package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/execctx"
)

// renderError writes a failure and returns the process exit code.
//
// Which stream it goes to is a deliberate choice. In a machine format the
// envelope goes to stdout, on the same stream as a successful payload, so a
// caller that always parses stdout and branches on "status" is correct in both
// cases. Sending it to stderr instead would hand `jq` an empty input and turn
// a clear failure into a confusing one. In human format the message goes to
// stderr, where a person expects it and where it cannot pollute a redirect.
func renderError(w io.Writer, errw io.Writer, ctx execctx.Context, err error) int {
	e := clierr.From(err)
	if e == nil {
		return clierr.ExitOK
	}
	if e.Reported {
		return e.ExitCode()
	}

	if ctx.Format.Machine() {
		if werr := e.WriteJSON(w); werr != nil {
			fmt.Fprintln(errw, "error:", e.Envelope.Error)
		}
		return e.ExitCode()
	}

	var b strings.Builder
	b.WriteString("error: ")
	b.WriteString(e.Envelope.Error)
	b.WriteString("\n")

	// Extra issues beyond the first, which is already the headline.
	if len(e.Envelope.Issues) > 1 {
		for _, is := range e.Envelope.Issues[1:] {
			if is.Message != "" {
				fmt.Fprintf(&b, "  %s\n", is.Message)
			}
		}
	}

	if len(e.Envelope.NextSteps) > 0 {
		b.WriteString("\ntry:\n")
		for _, s := range e.Envelope.NextSteps {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}

	// The request id is the one value support can act on, so it is shown
	// whenever the server gave us one, not only under --verbose.
	if e.Envelope.RequestID != "" {
		fmt.Fprintf(&b, "\nrequest id: %s\n", e.Envelope.RequestID)
	}

	if ctx.Verbose && e.Cause != nil {
		fmt.Fprintf(&b, "\ncause: %v\n", e.Cause)
	}

	fmt.Fprint(errw, b.String())
	return e.ExitCode()
}

// Render writes a failed command's error and returns the exit code. It is
// safe to call before the runtime is populated, which happens when the
// command line itself was malformed.
func Render(rt *Runtime, err error) int {
	if rt == nil {
		if isUsageError(err) {
			err = clierr.Wrap(err, "USAGE", err.Error()).WithExit(clierr.ExitUsage)
		}
		e := clierr.From(err)
		fmt.Fprintln(os.Stderr, "error:", e.Envelope.Error)
		return e.ExitCode()
	}
	if isUsageError(err) {
		err = clierr.Wrap(err, "USAGE", err.Error()).WithExit(clierr.ExitUsage)
	}
	if rt.Out == nil {
		// The failure happened before the output writer existed, which is
		// what a malformed command line looks like. Fall back to the streams
		// the caller supplied, never to the process globals.
		return renderError(rt.stdout, rt.stderr, rt.Ctx, err)
	}
	return renderError(rt.Out.Stdout(), rt.Out.Stderr(), rt.Ctx, err)
}

// isUsageError reports whether an error came from cobra's own argument or
// command parsing. Cobra exposes no sentinel for this, so the check is on the
// message; TestUnknownCommandIsAUsageError fails loudly if that wording ever
// changes, which is the point of having the test.
//
// It refuses to look at anything that is already a *clierr.Err. Those carry a
// stable code, a request id and an exit code that the server or the command
// chose deliberately, and the message is explicitly not part of the contract.
// A server that answered "invalid argument: metric foo is not known" would
// otherwise be reclassified as a local usage error, losing its code and its
// request id and claiming nothing was sent when in fact it was rejected.
func isUsageError(err error) bool {
	if err == nil {
		return false
	}
	var typed *clierr.Err
	if errors.As(err, &typed) {
		return false
	}
	msg := err.Error()
	for _, prefix := range []string{
		"unknown command",
		"unknown flag",
		"unknown shorthand flag",
		"invalid argument",
		"accepts ",
		"requires at least",
		"requires at most",
		"flag needs an argument",
	} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}
