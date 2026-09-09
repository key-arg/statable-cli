// Package clierr defines the single error shape the CLI emits and the exit
// codes that go with it.
//
// The contract has two halves and both matter:
//
//   - Machine callers branch on Issue.Code, never on prose. Codes are part of
//     the contract; Message wording is not and may be reworded at any time.
//   - The exit code separates "you can fix this" from "something broke".
//     A script that retries on 1 and pages a human on 2 is correct without
//     parsing anything.
package clierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Status distinguishes a recoverable condition from a failure.
type Status string

const (
	// StatusActionRequired means the caller can reach success by doing
	// something specific, which NextSteps names. Exits 1.
	StatusActionRequired Status = "action_required"
	// StatusError means the operation failed. Exits 2.
	StatusError Status = "error"
)

// Exit codes. These are part of the CLI's public contract.
const (
	// ExitOK is success.
	ExitOK = 0
	// ExitActionRequired is a recoverable condition: not logged in, no site
	// selected, nothing chosen. The caller can fix it and retry.
	ExitActionRequired = 1
	// ExitError is a failure: a bad request, a server error, a broken pipe.
	ExitError = 2
	// ExitThresholdNotMet is reserved for `statable check`. It exists so CI
	// can tell "the number was too low" apart from "the tool is broken",
	// which a shared exit code makes impossible.
	ExitThresholdNotMet = 3
	// ExitUsage is a malformed command line. Nothing was sent anywhere.
	ExitUsage = 64
)

// Issue is one machine-readable reason. Code is stable; Message is not.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Field   string `json:"field,omitempty"`
}

// Envelope is the whole of what a failing command writes to stdout when the
// output format is machine-readable, and what drives the human rendering
// otherwise.
type Envelope struct {
	Status    Status   `json:"status"`
	Error     string   `json:"error"`
	Issues    []Issue  `json:"issues,omitempty"`
	NextSteps []string `json:"next_steps,omitempty"`
	// RequestID is the server's X-Request-ID. It is the one value worth
	// keeping: its leading segment names the machine that served the call, so
	// support can find the log line from the id alone.
	RequestID string `json:"request_id,omitempty"`
}

// Err is an error carrying an Envelope. Commands return this; the root
// command renders it and picks the exit code.
type Err struct {
	Envelope Envelope
	// Exit overrides the code implied by Status. Zero means "derive it".
	Exit int
	// Cause is kept for --verbose and for errors.Is/errors.As.
	Cause error
	// Reported marks an error whose content the command has already written
	// itself. The renderer then contributes only the exit code. Without this,
	// a command that prints a rich status and also returns an error emits the
	// same failure twice, which in a machine format means two JSON objects on
	// stdout and an unparseable stream.
	Reported bool
}

func (e *Err) Error() string { return e.Envelope.Error }
func (e *Err) Unwrap() error { return e.Cause }

// Code returns the first issue code, or "" when there is none. Convenient for
// callers that only care about the primary reason.
func (e *Err) Code() string {
	if len(e.Envelope.Issues) == 0 {
		return ""
	}
	return e.Envelope.Issues[0].Code
}

// ExitCode resolves the process exit status for this error.
func (e *Err) ExitCode() int {
	if e.Exit != 0 {
		return e.Exit
	}
	if e.Envelope.Status == StatusActionRequired {
		return ExitActionRequired
	}
	return ExitError
}

// ActionRequired builds a recoverable error. NextSteps should be runnable
// commands, not prose: a caller, human or agent, should be able to copy one.
func ActionRequired(code, msg string, nextSteps ...string) *Err {
	return &Err{Envelope: Envelope{
		Status:    StatusActionRequired,
		Error:     msg,
		Issues:    []Issue{{Code: code, Message: msg}},
		NextSteps: nextSteps,
	}}
}

// Fail builds a failure.
func Fail(code, msg string, nextSteps ...string) *Err {
	return &Err{Envelope: Envelope{
		Status:    StatusError,
		Error:     msg,
		Issues:    []Issue{{Code: code, Message: msg}},
		NextSteps: nextSteps,
	}}
}

// Failf is Fail with formatting.
func Failf(code, format string, args ...any) *Err {
	return Fail(code, fmt.Sprintf(format, args...))
}

// Wrap turns an arbitrary error into an Err without losing the cause.
func Wrap(err error, code, msg string) *Err {
	e := Fail(code, msg)
	e.Cause = err
	return e
}

// From normalises any error into an *Err so the renderer has one shape to
// handle. An error that is already an *Err is returned unchanged.
func From(err error) *Err {
	if err == nil {
		return nil
	}
	var e *Err
	if errors.As(err, &e) {
		return e
	}
	return Wrap(err, "unexpected", err.Error())
}

// WriteJSON writes the envelope as a single JSON object followed by a newline.
func (e *Err) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(e.Envelope)
}

// Well-known codes the CLI raises itself. Server-side codes pass through
// unchanged from the API, which documents its own stable set.
const (
	CodeNotAuthenticated = "NOT_AUTHENTICATED"
	CodeNoSiteSelected   = "NO_SITE_SELECTED"
	CodeSiteNotFound     = "SITE_NOT_FOUND"
	CodeInvalidFormat    = "INVALID_FORMAT"
	CodeInvalidFilter    = "INVALID_FILTER"
	CodeRateLimited      = "RATE_LIMITED"
	CodeNetwork          = "NETWORK"
	CodeServer           = "SERVER"
	CodeKeyringLocked    = "KEYRING_UNAVAILABLE"
	CodeUnexpected       = "unexpected"
)

// WithExit overrides the exit code implied by Status. Used for usage errors,
// which are neither an action the user can retry nor a server failure.
func (e *Err) WithExit(code int) *Err {
	e.Exit = code
	return e
}

// Silent returns an error that carries an exit code and nothing else. Use it
// when the command has already written everything the caller needs.
func Silent(code int) *Err {
	return &Err{Envelope: Envelope{Status: StatusError}, Exit: code, Reported: true}
}
