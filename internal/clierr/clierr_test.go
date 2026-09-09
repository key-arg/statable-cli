package clierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestExitCodesSeparateRecoveryFromFailure is the contract a script depends
// on: retry on 1, page a human on 2.
func TestExitCodesSeparateRecoveryFromFailure(t *testing.T) {
	if got := ActionRequired("X", "m").ExitCode(); got != ExitActionRequired {
		t.Fatalf("action_required exits %d, want %d", got, ExitActionRequired)
	}
	if got := Fail("X", "m").ExitCode(); got != ExitError {
		t.Fatalf("error exits %d, want %d", got, ExitError)
	}
	if got := Fail("X", "m").WithExit(ExitUsage).ExitCode(); got != ExitUsage {
		t.Fatalf("an explicit override should win, got %d", got)
	}
}

// TestExitCodesAreDistinct catches a copy-paste that would make CI unable to
// tell a failed threshold from a broken tool.
func TestExitCodesAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, code := range map[string]int{
		"ok":              ExitOK,
		"action_required": ExitActionRequired,
		"error":           ExitError,
		"threshold":       ExitThresholdNotMet,
		"usage":           ExitUsage,
	} {
		if prev, ok := seen[code]; ok {
			t.Fatalf("%s and %s share exit code %d", prev, name, code)
		}
		seen[code] = name
	}
}

func TestEnvelopeCarriesAStableCode(t *testing.T) {
	e := Fail("unknown_metric", "unknown metric: foo")
	if e.Code() != "unknown_metric" {
		t.Fatalf("Code() = %q", e.Code())
	}
	if Fail("", "m").Code() != "" {
		t.Fatal("an envelope with no issues has no code")
	}
}

func TestFromPreservesAnExistingErr(t *testing.T) {
	orig := ActionRequired("X", "m", "do this")
	wrapped := fmt.Errorf("context: %w", orig)
	got := From(wrapped)
	if got != orig {
		t.Fatal("From must unwrap to the original *Err rather than re-wrapping it")
	}
	if From(nil) != nil {
		t.Fatal("From(nil) must be nil")
	}
}

func TestFromNormalisesAPlainError(t *testing.T) {
	e := From(errors.New("something broke"))
	if e.Envelope.Status != StatusError {
		t.Fatalf("status = %q", e.Envelope.Status)
	}
	if e.ExitCode() != ExitError {
		t.Fatalf("exit = %d", e.ExitCode())
	}
	if !errors.Is(e, e.Cause) {
		t.Fatal("the cause must remain reachable through errors.Is")
	}
}

func TestWriteJSONIsOneLineAndParseable(t *testing.T) {
	e := ActionRequired(CodeNotAuthenticated, "no API key is configured", "statable auth login")
	e.Envelope.RequestID = "ch-node01-abc"

	var b bytes.Buffer
	if err := e.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimRight(b.String(), "\n"), "\n") != 0 {
		t.Fatalf("the envelope should be one line, got %q", b.String())
	}

	var got Envelope
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("envelope does not parse: %v", err)
	}
	if got.Status != StatusActionRequired || got.RequestID != "ch-node01-abc" {
		t.Fatalf("envelope = %+v", got)
	}
	if len(got.NextSteps) != 1 {
		t.Fatalf("next steps = %v", got.NextSteps)
	}
}

func TestEmptyFieldsAreOmitted(t *testing.T) {
	var b bytes.Buffer
	if err := (&Err{Envelope: Envelope{Status: StatusError, Error: "m"}}).WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"issues", "next_steps", "request_id"} {
		if strings.Contains(b.String(), k) {
			t.Fatalf("%q should be omitted when empty, got %s", k, b.String())
		}
	}
}

// TestSilentCarriesOnlyACode: used when the command has already printed
// everything, so the renderer must not write a second time.
func TestSilentCarriesOnlyACode(t *testing.T) {
	e := Silent(ExitThresholdNotMet)
	if !e.Reported {
		t.Fatal("Silent must be marked as already reported")
	}
	if e.ExitCode() != ExitThresholdNotMet {
		t.Fatalf("exit = %d", e.ExitCode())
	}
}

// TestEnvelopeWithNoIssuesIsSafe checks what this package guarantees about an
// envelope carrying no issues. The renderer that walks Issues[1:] lives in
// package cli and is covered there; this only pins the accessors, which is
// what the comment used to claim more than it did.
func TestEnvelopeWithNoIssuesIsSafe(t *testing.T) {
	e := &Err{Envelope: Envelope{Status: StatusError, Error: "boom"}}
	if e.Code() != "" {
		t.Fatal("an envelope with no issues has no code")
	}
	var b bytes.Buffer
	if err := e.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if e.ExitCode() != ExitError {
		t.Fatalf("exit = %d", e.ExitCode())
	}
}
