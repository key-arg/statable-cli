package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/key-arg/statable-cli/internal/auth"
)

// run executes the CLI in-process against real pipes, which is what a script
// or CI sees: no terminal on any stream. It returns stdout, stderr and the
// exit code.
func run(t *testing.T, env map[string]string, args ...string) (string, string, int) {
	t.Helper()

	for k, v := range env {
		t.Setenv(k, v)
	}

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	root, rt := NewRoot(outW, errW)
	root.SetArgs(args)
	root.SetOut(outW)
	root.SetErr(errW)

	// The pipes are drained while the command runs. Reading only afterwards
	// deadlocks the moment output exceeds the pipe buffer, which turns a
	// large-output regression into a hung test rather than a failing one.
	var outB, errB strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(&outB, outR) }()
	go func() { defer wg.Done(); io.Copy(&errB, errR) }()

	code := 0
	if err := root.ExecuteContext(context.Background()); err != nil {
		code = Render(rt, err)
	}

	outW.Close()
	errW.Close()
	wg.Wait()
	outR.Close()
	errR.Close()

	lastRuntime = rt
	return outB.String(), errB.String(), code
}

// lastRuntime is the Runtime the most recent run built. A test that has to
// re-sign a cache file uses it so the signing goes through the production
// code rather than a copy of the formula, which would keep passing if the
// formula changed.
var lastRuntime *Runtime

// resignSiteCache rewrites a cache file with a new timestamp and a valid MAC.
func resignSiteCache(t *testing.T, path string, fetchedAt time.Time) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f siteCacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	f.FetchedAt = fetchedAt
	payload, err := macPayload(f.FetchedAt, f.Sites)
	if err != nil {
		t.Fatal(err)
	}
	f.MAC = lastRuntime.siteCacheMAC(context.Background(), payload)
	if f.MAC == "" {
		t.Fatal("the runtime could not sign the cache; the key did not resolve")
	}
	b, _ = json.Marshal(f)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func emptyConfig(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"STATABLE_CONFIG_DIR": t.TempDir(),
		"STATABLE_API_KEY":    "",
		// Never the developer's real keyring: resolving through it made two
		// runs in this session fail for reasons unrelated to the code, and it
		// would do the same on a CI machine that has one.
		auth.NoKeyringVar: "1",
	}
}

func TestVersionJSON(t *testing.T) {
	out, _, code := run(t, emptyConfig(t), "version", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, out)
	}
	// The version is the field a caller branches on, and it is never empty:
	// a build with no tag and no module version still reports its commit.
	if s, _ := v["version"].(string); s == "" {
		t.Fatalf("version must always name something, got %v", v["version"])
	}
	for _, k := range []string{"go", "os", "arch"} {
		if s, _ := v[k].(string); s == "" {
			t.Fatalf("%s is missing from %v", k, v)
		}
	}
}

// TestLoginWithoutTTYEmitsExactlyOneObject is a regression test. An earlier
// version printed an instructions payload and then the error envelope, so a
// caller piping into jq got two objects and a parse failure.
func TestLoginWithoutTTYEmitsExactlyOneObject(t *testing.T) {
	out, _, code := run(t, emptyConfig(t), "auth", "login", "--json")

	dec := json.NewDecoder(strings.NewReader(out))
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("first object does not parse: %v (%q)", err, out)
	}
	var extra map[string]any
	if err := dec.Decode(&extra); err == nil {
		t.Fatalf("a second object was written to stdout: %v", extra)
	}

	if first["status"] != "action_required" {
		t.Fatalf("status = %v, want action_required", first["status"])
	}
	steps, _ := first["next_steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("headless login must name what to do next")
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a recoverable state", code)
	}
}

// TestLoginWithoutTTYDoesNotBlock: the whole point of the non-interactive
// path. If this test ever hangs, the regression is fatal for CI users.
func TestLoginWithoutTTYDoesNotBlock(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(t, emptyConfig(t), "auth", "login")
	}()
	select {
	case <-done:
	case <-timeoutAfter():
		t.Fatal("auth login blocked without a terminal")
	}
}

func TestUnknownFormatIsAUsageError(t *testing.T) {
	_, errOut, code := run(t, emptyConfig(t), "--format", "xml", "version")
	if code != 64 {
		t.Fatalf("exit = %d, want 64 for a malformed command line", code)
	}
	if !strings.Contains(errOut, "xml") {
		t.Fatalf("the message should name the bad value, got %q", errOut)
	}
}

// TestStatusExitCodeIsFormatIndependent: the same condition must mean the same
// thing to a person and to a script. An earlier version exited 0 in JSON and
// 2 in human output for an unusable key.
func TestStatusExitCodeIsFormatIndependent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"unauthorized","error":"bad key"}`))
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_bogus"
	env["STATABLE_API_URL"] = srv.URL

	outJSON, _, codeJSON := run(t, env, "auth", "status", "--json")
	_, _, codeHuman := run(t, env, "auth", "status")

	if codeJSON != codeHuman {
		t.Fatalf("exit codes differ by format: json=%d human=%d", codeJSON, codeHuman)
	}
	if codeJSON == 0 {
		t.Fatal("an unusable key must not exit 0")
	}

	var v map[string]any
	if err := json.Unmarshal([]byte(outJSON), &v); err != nil {
		t.Fatalf("status output is not valid JSON: %v (%q)", err, outJSON)
	}
	if v["authenticated"] != false {
		t.Fatalf("authenticated = %v, want false", v["authenticated"])
	}
}

func TestStatusSucceedsWithAGoodKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1200")
		w.Header().Set("X-RateLimit-Remaining", "1199")
		w.Write([]byte(`{"sites":[{"id":1,"domain":"example.com"}]}`))
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_good"
	env["STATABLE_API_URL"] = srv.URL

	out, _, code := run(t, env, "auth", "status", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v["authenticated"] != true {
		t.Fatalf("authenticated = %v", v["authenticated"])
	}
	if v["source"] != "environment" {
		t.Fatalf("source = %v, want the environment to be named", v["source"])
	}
	if _, ok := v["rate_limit"]; !ok {
		t.Fatal("the reported budget should come from the response headers")
	}
	if s, _ := v["key"].(string); strings.Contains(s, "good") {
		t.Fatalf("the key must be redacted, got %q", s)
	}
}

// TestProjectConfigWarningGoesToStderr: a refused key has to be visible, but
// never on the payload stream.
func TestProjectConfigWarningGoesToStderr(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".statable.yml"),
		[]byte("site: example.com\napi_url: https://evil.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	out, errOut, _ := run(t, emptyConfig(t), "version", "--json")

	if !strings.Contains(errOut, "api_url") {
		t.Fatalf("the refused key must be reported on stderr, got %q", errOut)
	}
	if strings.Contains(out, "api_url") {
		t.Fatalf("warnings must not reach stdout, got %q", out)
	}
	if err := json.Unmarshal([]byte(out), &map[string]any{}); err != nil {
		t.Fatalf("stdout must stay parseable despite the warning: %v", err)
	}
}

// TestUnknownCommandIsAUsageError: a malformed command line is exit 64 in
// every shape. An earlier version exited 64 for a bad flag but 2 for a bad
// command, so a caller could not tell "I typed it wrong" from "it broke".
//
// This test also guards the string matching in isUsageError: if cobra ever
// rewords these messages, this fails rather than silently regressing.
func TestUnknownCommandIsAUsageError(t *testing.T) {
	cases := [][]string{
		{"nosuchcommand"},
		{"--nosuchflag"},
		{"version", "unexpected-argument"},
		{"--format", "xml", "version"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, code := run(t, emptyConfig(t), args...)
			if code != 64 {
				t.Fatalf("exit = %d, want 64 for a malformed command line", code)
			}
		})
	}
}

// TestBrokenProjectConfigIsReported: a file that does not parse must say so.
// Silently contributing nothing leaves the user to wonder why their setting
// has no effect.
func TestBrokenProjectConfigIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".statable.yml"),
		[]byte("site: [unclosed\n  x: :\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	out, errOut, code := run(t, emptyConfig(t), "version", "--json")
	if code != 0 {
		t.Fatalf("a broken project file must not fail the command, exit = %d", code)
	}
	if !strings.Contains(errOut, "could not be parsed") {
		t.Fatalf("the parse failure must be reported on stderr, got %q", errOut)
	}
	if err := json.Unmarshal([]byte(out), &map[string]any{}); err != nil {
		t.Fatalf("stdout must stay parseable: %v (%q)", err, out)
	}
}

// TestCSVIsNotSilentlyJSON: asking for CSV and receiving JSON is a silent
// contract break for any script that pipes into cut or awk.
func TestCSVIsNotSilentlyJSON(t *testing.T) {
	out, _, code := run(t, emptyConfig(t), "version", "--format", "csv")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("--format csv produced JSON: %q", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "version,") {
		t.Fatalf("want a header and one row, got %q", out)
	}
}

// TestHelpNeverReachesStdoutInMachineFormats: cobra writes help through its
// own writer, bypassing the io.Discard mechanism entirely. A bare `statable
// --json` used to print the long description to stdout and exit 0, so a
// caller piping into jq got a parse error and a success code.
func TestHelpNeverReachesStdoutInMachineFormats(t *testing.T) {
	for _, args := range [][]string{
		{"--json"},
		{"--format", "csv"},
		{"auth", "--json"},
		{"--help", "--json"},
		{"auth", "--help", "--format", "csv"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, _, code := run(t, emptyConfig(t), args...)
			if strings.Contains(out, "Usage:") || strings.Contains(out, "Read your Statable") {
				t.Fatalf("help text reached stdout: %q", out)
			}
			if s := strings.TrimSpace(out); s != "" {
				if err := json.Unmarshal([]byte(s), &map[string]any{}); err != nil {
					t.Fatalf("stdout must be empty or parseable, got %q", s)
				}
			}
			// An explicit --help is a request that succeeded; a bare
			// invocation with no command is a usage error. Discarding the
			// code left both unpinned.
			wantOK := false
			for _, a := range args {
				if a == "--help" {
					wantOK = true
				}
			}
			if wantOK && code != 0 {
				t.Fatalf("--help is a request that succeeded, exit = %d", code)
			}
			if !wantOK && code == 0 {
				t.Fatalf("a bare invocation in a machine format must not exit 0")
			}
		})
	}
}

// TestBareInvocationInMachineFormatIsNotSuccess: "no command given" is not a
// result a script should read as success.
func TestBareInvocationInMachineFormatIsNotSuccess(t *testing.T) {
	_, _, code := run(t, emptyConfig(t), "--json")
	if code == 0 {
		t.Fatal("a bare invocation in a machine format must not exit 0")
	}
}

// TestServerErrorKeepsItsCodeAndRequestID: the usage-error sniffing looks at
// message text, and a server message may legitimately start with the same
// words. It must never reclassify an error that already carries a code.
func TestServerErrorKeepsItsCodeAndRequestID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "ch-node07-REQID")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":"unknown_metric","error":"invalid argument: metric foo is not known","request_id":"ch-node07-REQID"}`))
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	out, _, code := run(t, env, "auth", "status", "--json")

	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output does not parse: %v (%q)", err, out)
	}
	if v["code"] != "unknown_metric" {
		t.Fatalf("code = %v, want the server's own stable code", v["code"])
	}
	if v["request_id"] != "ch-node07-REQID" {
		t.Fatalf("request_id = %v, it must survive into the output", v["request_id"])
	}
	if code == 64 {
		t.Fatal("a request the server rejected is not a local usage error")
	}
}

// TestUnauthenticatedStatusCarriesNextSteps: the payload must name an action.
func TestUnauthenticatedStatusCarriesNextSteps(t *testing.T) {
	out, _, _ := run(t, emptyConfig(t), "auth", "status", "--json")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	steps, _ := v["next_steps"].([]any)
	if len(steps) == 0 {
		t.Fatalf("an unauthenticated status must name what to do, got %v", v)
	}
	// The environment variable is safer than --key, which is visible in ps
	// and in shell history, so it has to come first.
	if first, _ := steps[0].(string); !strings.Contains(first, "STATABLE_API_KEY") {
		t.Fatalf("the environment variable should be suggested first, got %v", steps)
	}
}

// TestEveryMachineCommandHonoursCSV: three commands reached for JSON directly
// and silently returned it to a caller who asked for CSV.
func TestEveryMachineCommandHonoursCSV(t *testing.T) {
	for _, args := range [][]string{
		{"version"},
		{"auth", "status"},
		{"auth", "logout"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, _, _ := run(t, emptyConfig(t), append(args, "--format", "csv")...)
			s := strings.TrimSpace(out)
			if s == "" {
				t.Fatal("no output at all")
			}
			if strings.HasPrefix(s, "{") {
				t.Fatalf("--format csv produced JSON: %q", s)
			}
			if _, err := csv.NewReader(strings.NewReader(out)).ReadAll(); err != nil {
				t.Fatalf("output is not CSV: %v (%q)", err, out)
			}
		})
	}
}

// TestJSONAndFormatConflictIsRefused: two flags asking for different things
// must not have one silently win.
func TestJSONAndFormatConflictIsRefused(t *testing.T) {
	_, _, code := run(t, emptyConfig(t), "version", "--json", "--format", "csv")
	if code != 64 {
		t.Fatalf("exit = %d, want 64 for contradictory flags", code)
	}
	_, _, ok := run(t, emptyConfig(t), "version", "--json", "--format", "json")
	if ok != 0 {
		t.Fatalf("agreeing flags must be accepted, exit = %d", ok)
	}
}

// TestVersionDoesNotTouchTheKeyring: resolving credentials for every command
// made `statable version` wait on a locked keyring for the full timeout.
//
// A wall-clock assertion could not fail here, because the real keyring answers
// instantly on every developer machine and every CI runner. The check is
// therefore structural: the command must not have resolved a key at all.
func TestVersionDoesNotTouchTheKeyring(t *testing.T) {
	env := emptyConfig(t)
	for k, v := range env {
		t.Setenv(k, v)
	}

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { outR.Close(); errR.Close() }()

	root, rt := NewRoot(outW, errW)
	root.SetArgs([]string{"version", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("version failed: %v", err)
	}
	outW.Close()
	errW.Close()

	if rt.keyResolved() {
		t.Fatal("version resolved the credential; on a locked keyring that is a full-timeout wait for a command that needs no key")
	}
}

// TestAuthStatusExitsNonZeroWhenNotSignedIn: `statable auth status && deploy`
// must stop rather than proceed without a key. gh sets the same precedent.
// The report is still printed in full; only the exit code carries the verdict.
func TestAuthStatusExitsNonZeroWhenNotSignedIn(t *testing.T) {
	for _, format := range []string{"human", "json", "csv"} {
		t.Run(format, func(t *testing.T) {
			out, _, code := run(t, emptyConfig(t), "auth", "status", "--format", format)
			if code != 1 {
				t.Fatalf("exit = %d, want 1: not signed in is an action the caller can take", code)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatal("the state must still be reported, not only signalled by the code")
			}
			if format == "json" {
				var v map[string]any
				if err := json.Unmarshal([]byte(out), &v); err != nil {
					t.Fatalf("not JSON: %v (%q)", err, out)
				}
				if v["authenticated"] != false {
					t.Fatalf("authenticated = %v", v["authenticated"])
				}
			}
		})
	}
}

// TestInsecureStorageFlagStillReachesTheStore pins the wiring, not the policy.
//
// --insecure-storage used to be a root persistent flag, which put it in the
// help of every command it has no effect on. Moving it onto `auth login` is
// only safe because a subcommand's flags are parsed before the runtime builds
// the credential store; if that order ever changes, the flag silently stops
// working and the key is not written at all. This test fails loudly in that
// case, which a help-text assertion would not.
func TestInsecureStorageFlagStillReachesTheStore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC"}]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{
		"STATABLE_CONFIG_DIR": dir,
		"STATABLE_API_KEY":    "",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	_, _, code := run(t, env, "auth", "login",
		"--key", "stbl_test_key_long_enough", "--insecure-storage", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	path := filepath.Join(dir, "credentials.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the key was not written to %s, so the flag never reached the store: %v", path, err)
	}
	// See auth.FileModeIsEnforced: Windows does not honour the bits.
	if perm := fi.Mode().Perm(); auth.FileModeIsEnforced() && perm != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", perm)
	}
}

// TestInsecureStorageIsNotOfferedWhereItDoesNothing: a flag that appears in a
// command's help and changes nothing is the same defect as a setting that is
// parsed and ignored, which this codebase refuses to ship elsewhere.
func TestInsecureStorageIsNotOfferedWhereItDoesNothing(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"now", "--help"}, {"sites", "--help"}} {
		out, errOut, _ := run(t, emptyConfig(t), args...)
		if strings.Contains(out+errOut, "insecure-storage") {
			t.Fatalf("`statable %s` offers --insecure-storage, which only auth login reads",
				strings.Join(args, " "))
		}
	}
	out, errOut, _ := run(t, emptyConfig(t), "auth", "login", "--help")
	if !strings.Contains(out+errOut, "insecure-storage") {
		t.Fatal("auth login must still offer --insecure-storage")
	}
}

// TestPreFlightErrorsExitAsUsage pins the exit-code contract.
//
// clierr defines 64 as "a malformed command line; nothing was sent anywhere",
// and every case below is caught before a request exists. They used to be
// split: a bad limit exited 64 while a bad metric exited 2, the code reserved
// for "something broke". A script could therefore not tell a typo from an
// outage, which is the one distinction the exit code exists to make.
func TestPreFlightErrorsExitAsUsage(t *testing.T) {
	cases := [][]string{
		{"stats", "-m", "nonsense"},
		{"stats", "--filter", "bogus"},
		{"stats", "--range", "nonsense"},
		{"stats", "--compare", "nonsense"},
		{"stats", "--range", "2026-01-01..2026-01-31", "--compare", "2025-01-01..2025-01-10"},
		{"series", "--by", "minute", "--range", "30d"},
		{"top", "pages", "-n", "5000"},
		{"top", "pages", "-n", "0"},
		{"top", "pages", "--offset", "-1"},
		{"query", "--dimension", "bogus:dim", "-m", "visitors"},
		{"props", "--range", "nonsense"},
		{"funnel", "abc"},
		{"funnel", "45", "--filter", "event=Signup"},
	}
	env := emptyConfig(t)
	// A key has to exist, or every case would stop at "not authenticated"
	// before reaching the validation this test is about.
	env["STATABLE_API_KEY"] = "stbl_test_key_long_enough"
	// An address nothing listens on: if validation ever stops catching one of
	// these, the request fails with a network error and a different code,
	// which is exactly the regression worth seeing.
	env["STATABLE_API_URL"] = "http://127.0.0.1:1/api/v1"

	for _, args := range cases {
		_, _, code := run(t, env, args...)
		if code != 64 {
			t.Errorf("`statable %s` exited %d, want 64", strings.Join(args, " "), code)
		}
	}
}

// countingServer records every path it is asked for.
type countingServer struct {
	mu     sync.Mutex
	paths  []string
	siteID int64
	// staleFor rejects this id on /current-visitors with unknown_site, which
	// is what the API returns for a site the key cannot reach.
	staleFor int64
}

func (c *countingServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.paths = append(c.paths, r.URL.Path)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sites"):
			fmt.Fprintf(w, `{"sites":[{"site_id":%d,"name":"example.com","timezone":"UTC"}]}`, c.siteID)
		case strings.HasSuffix(r.URL.Path, "/current-visitors"):
			if id := r.URL.Query().Get("site_id"); c.staleFor != 0 && id == strconv.FormatInt(c.staleFor, 10) {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"code":"unknown_site","error":"no such site"}`)
				return
			}
			io.WriteString(w, `{"visitors":42}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"not_found","error":"nope"}`)
		}
	}
}

func (c *countingServer) count(suffix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

// TestSiteListingIsNotFetchedTwice: the Stats API needs a numeric site id and
// people name sites by domain, so every reading command listed the sites
// first. A `statable now` in a shell prompt therefore spent two calls an hour
// where one would do.
func TestSiteListingIsNotFetchedTwice(t *testing.T) {
	cs := &countingServer{siteID: 7}
	srv := httptest.NewServer(cs.handler())
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{
		"STATABLE_CONFIG_DIR": dir,
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	if _, _, code := run(t, env, "now"); code != 0 {
		t.Fatalf("first now exited %d", code)
	}
	if n := cs.count("/sites"); n != 1 {
		t.Fatalf("the first call listed sites %d times, want 1", n)
	}
	if _, _, code := run(t, env, "now"); code != 0 {
		t.Fatalf("second now exited %d", code)
	}
	if n := cs.count("/sites"); n != 1 {
		t.Fatalf("a warm cache still listed sites: %d calls in total, want 1", n)
	}
}

// TestSiteCacheIsNotSharedBetweenKeys: two keys see different sites, so a
// listing cached under one must never answer for the other.
func TestSiteCacheIsNotSharedBetweenKeys(t *testing.T) {
	cs := &countingServer{siteID: 7}
	srv := httptest.NewServer(cs.handler())
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{
		"STATABLE_CONFIG_DIR": dir,
		"STATABLE_API_KEY":    "stbl_first_key_long_enough",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	run(t, env, "now")
	env["STATABLE_API_KEY"] = "stbl_second_key_long_enough"
	run(t, env, "now")
	if n := cs.count("/sites"); n != 2 {
		t.Fatalf("sites listed %d times, want 2: a second key reused the first key's cache", n)
	}
}

// TestStaleSiteCacheRepairsItself: ids are stable but not eternal. A cached
// listing that the server no longer agrees with must be refreshed and the call
// retried, or a rename leaves the CLI failing for a whole day with an error
// that explains nothing.
func TestStaleSiteCacheRepairsItself(t *testing.T) {
	cs := &countingServer{siteID: 7}
	srv := httptest.NewServer(cs.handler())
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{
		"STATABLE_CONFIG_DIR": dir,
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	run(t, env, "now")

	// The site now answers under a different id, and the cached one is refused.
	cs.mu.Lock()
	cs.siteID, cs.staleFor = 9, 7
	cs.mu.Unlock()

	out, _, code := run(t, env, "now")
	if code != 0 {
		t.Fatalf("a stale cache must repair itself, exit = %d (%q)", code, out)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("the retry returned nothing useful: %q", out)
	}
}

// TestSiteCacheExpires: a listing believed forever is a listing that goes
// wrong silently. A stale entry is only repaired when the server contradicts
// it, so the age check is the only thing that notices a site added elsewhere.
func TestSiteCacheExpires(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		refetch bool
	}{
		{"fresh", time.Hour, false},
		{"expired", siteCacheTTL + time.Hour, true},
		// A clock that moved backwards, or a file written by a machine ahead
		// of this one, gives an age no rule can use. Treating it as valid
		// would trust it until the clock caught up.
		{"written in the future", -time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := &countingServer{siteID: 7}
			srv := httptest.NewServer(cs.handler())
			defer srv.Close()

			dir := t.TempDir()
			env := map[string]string{
				"STATABLE_CONFIG_DIR": dir,
				"STATABLE_API_KEY":    "stbl_test_key_long_enough",
				auth.NoKeyringVar:     "1",
				"STATABLE_API_URL":    srv.URL + "/api/v1",
			}
			run(t, env, "now")
			if cs.count("/sites") != 1 {
				t.Fatalf("setup listed sites %d times", cs.count("/sites"))
			}

			// Re-signed, because an edited file is a miss by design and
			// this test is about the age rule, not about the signature.
			resignSiteCache(t, filepath.Join(dir, "sites-cache.json"),
				time.Now().Add(-tc.age))

			run(t, env, "now")
			got := cs.count("/sites")
			want := 1
			if tc.refetch {
				want = 2
			}
			if got != want {
				t.Fatalf("sites listed %d times, want %d", got, want)
			}
		})
	}
}

// TestCompletionsProposeValues: the generated completion scripts existed and
// proposed nothing, which is worse than no completion at all — the shell falls
// back to file names and offers the current directory's contents as a metric.
func TestCompletionsProposeValues(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "top", ""}, "pages"},
		{[]string{"__complete", "--format", ""}, "json"},
		{[]string{"__complete", "stats", "--format", ""}, "csv"},
		{[]string{"__complete", "stats", "--metric", ""}, "visitors"},
		{[]string{"__complete", "stats", "--range", ""}, "7d"},
		{[]string{"__complete", "stats", "--compare", ""}, "previous_period"},
		{[]string{"__complete", "series", "--by", ""}, "day"},
		{[]string{"__complete", "series", "--marker", ""}, "braille"},
		{[]string{"__complete", "query", "--dimension", ""}, "visit:country"},
	}
	for _, tc := range cases {
		out, _, _ := run(t, emptyConfig(t), tc.args...)
		if !strings.Contains(out, tc.want) {
			t.Errorf("`%s` proposed no %q:\n%s", strings.Join(tc.args, " "), tc.want, out)
		}
		// The last line is the directive, as a number. 4 is NoFileComp: a
		// closed value set must not let the shell fall back to file names and
		// offer the current directory's contents as a metric.
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if last := lines[len(lines)-1]; last != ":4" {
			t.Errorf("`%s` ended with directive %q, want \":4\" (no file completion)",
				strings.Join(tc.args, " "), last)
		}
	}
}

// TestCompareCompletionOffersOnlyWhatTheAPIAccepts: the API takes
// "previous_period" or an explicit pair, and nothing else. A third value in
// the completion list is a command that fails after the user tabbed to it.
func TestCompareCompletionOffersOnlyWhatTheAPIAccepts(t *testing.T) {
	out, _, _ := run(t, emptyConfig(t), "__complete", "stats", "--compare", "")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") ||
			strings.HasPrefix(line, "Completion ended") {
			continue
		}
		if line != "previous_period" {
			t.Errorf("--compare completion offers %q, which the API rejects", line)
		}
	}
}

// TestNextStepsAreRunnableCommands: next_steps is the actionable half of the
// error envelope, and an agent or a script may run what it finds there. The
// root's own name is "statable", so appending it produced the step "statable
// statable --help", which is not a command.
func TestNextStepsAreRunnableCommands(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"auth", "--json"}} {
		out, _, _ := run(t, emptyConfig(t), args...)
		var env map[string]any
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("`statable %s` did not emit an envelope: %v (%q)",
				strings.Join(args, " "), err, out)
		}
		steps, _ := env["next_steps"].([]any)
		if len(steps) == 0 {
			t.Fatalf("`statable %s` names nothing to do next", strings.Join(args, " "))
		}
		for _, s := range steps {
			step, _ := s.(string)
			fields := strings.Fields(step)
			if len(fields) < 2 || fields[0] != "statable" {
				t.Errorf("next step %q is not a statable command", step)
				continue
			}
			// "statable statable ..." names the binary twice.
			if fields[1] == "statable" {
				t.Errorf("next step %q repeats the program name", step)
			}
		}
	}
}

// TestNoCommandAcceptsAFlagItIgnores: a flag that is typed, parsed and thrown
// away is the same defect as a setting that silently does nothing, and it is
// worse than an error because the user believes it worked. Hiding such a flag
// from help is not a fix: it is still accepted.
//
// This covers per-command flags only. The root's persistent flags (--site,
// --format, --quiet) are global by design and are accepted everywhere, even
// where they have nothing to act on.
func TestNoCommandAcceptsAFlagItIgnores(t *testing.T) {
	cases := []struct {
		args []string
		why  string
	}{
		// GET /props reads site_id and date_range and nothing else.
		{[]string{"props", "--filter", "country=US"}, "/props takes no filters"},
		{[]string{"props", "--compare", "previous_period"}, "/props has no comparison"},
		// A funnel report examines sessions; it has no comparison window.
		{[]string{"funnel", "1", "--compare", "previous_period"}, "a funnel report has no comparison"},
		{[]string{"funnels", "--range", "7d"}, "a definition listing has no period"},
		{[]string{"subscription", "--range", "7d"}, "the plan is not read per period"},
	}
	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_test_key_long_enough"
	env["STATABLE_API_URL"] = "http://127.0.0.1:1/api/v1"

	for _, tc := range cases {
		_, errOut, code := run(t, env, tc.args...)
		if code != 64 || !strings.Contains(errOut, "unknown flag") {
			t.Errorf("`statable %s` was accepted (exit %d) but %s",
				strings.Join(tc.args, " "), code, tc.why)
		}
	}
}

// TestEmptyServerAnswersAreReportedNotPrintedBlank: exiting 0 with no output
// looks like success with no answer. A saved funnel always has steps and the
// subscription endpoint always sets a status, so neither is legitimately
// empty; a response missing them did not answer.
func TestEmptyServerAnswersAreReportedNotPrintedBlank(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		args []string
	}{
		{"funnel report with no steps", `{}`, []string{"funnel", "1"}},
		{"subscription with no status", `{}`, []string{"subscription"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/sites") {
					io.WriteString(w, `{"sites":[{"site_id":7,"name":"example.com","timezone":"UTC"}]}`)
					return
				}
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			env := map[string]string{
				"STATABLE_CONFIG_DIR": t.TempDir(),
				"STATABLE_API_KEY":    "stbl_test_key_long_enough",
				auth.NoKeyringVar:     "1",
				"STATABLE_API_URL":    srv.URL + "/api/v1",
			}
			out, _, code := run(t, env, tc.args...)
			if code == 0 {
				t.Fatalf("exit 0 with output %q; an unanswerable response must not read as success", out)
			}
		})
	}
}

// TestAbsentEndsAtIsNullNotEmpty: the API declares ends_at as a pointer with
// omitempty precisely so a client can tell "unknown" from a value, and it is
// absent for every account with no subscription. Collapsing that to "" tells
// a caller the date is known and blank, so `jq 'select(.ends_at == null)'`
// matches nothing and every consumer has to special-case the empty string.
func TestAbsentEndsAtIsNullNotEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"none","is_trial":false,"only_hobby":true}`)
	}))
	defer srv.Close()

	env := map[string]string{
		"STATABLE_CONFIG_DIR": t.TempDir(),
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	out, _, code := run(t, env, "subscription", "--json")
	if code != 0 {
		t.Fatalf("exit = %d (%q)", code, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out)
	}
	v, present := got["ends_at"]
	if !present {
		t.Fatal("ends_at must be present so a consumer can read it unconditionally")
	}
	if v != nil {
		t.Fatalf("ends_at = %#v, want null: an absent date is not an empty one", v)
	}
}

// TestMachineFormatsKeepRawValues: a person reads grouped numbers and a short
// date; a machine must get the number and the instant exactly as they arrived.
// Formatting a cell for both audiences is how a CSV column stops being
// parseable and a JSON number becomes a string.
func TestMachineFormatsKeepRawValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":7,"name":"example.com","timezone":"UTC"}]}`)
			return
		}
		io.WriteString(w, `{"props":[{"key":"plan","event":"Signup","count":1234567,`+
			`"first_seen":"2026-06-01T10:00:00.123456789Z"}]}`)
	}))
	defer srv.Close()

	env := map[string]string{
		"STATABLE_CONFIG_DIR": t.TempDir(),
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}

	out, _, code := run(t, env, "props", "--json")
	if code != 0 {
		t.Fatalf("exit = %d (%q)", code, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out)
	}
	if got, ok := rows[0]["count"].(float64); !ok || got != 1234567 {
		t.Fatalf("count = %#v, want the number 1234567", rows[0]["count"])
	}
	if got := rows[0]["first_seen"]; got != "2026-06-01T10:00:00.123456789Z" {
		t.Fatalf("first_seen = %#v, want the instant unchanged", got)
	}

	out, _, _ = run(t, env, "props", "--format", "csv")
	if !strings.Contains(out, "1234567") || strings.Contains(out, "1 234 567") {
		t.Fatalf("CSV must carry the raw count:\n%s", out)
	}

	// The human table is the only place formatting belongs. The separator is
	// a thin space, so the assertion is on the grouping rather than on the
	// exact glyph, which is a typographic choice this test does not pin.
	out, _, _ = run(t, env, "props")
	if strings.Contains(out, "1234567") {
		t.Fatalf("a person should see a grouped number:\n%s", out)
	}
	if !regexp.MustCompile(`1\D234\D567`).MatchString(out) {
		t.Fatalf("the grouped number is not there at all:\n%s", out)
	}
	if strings.Contains(out, ".123456789") {
		t.Fatalf("nine digits of precision are not a date anyone reads:\n%s", out)
	}
}

// TestPoisonedSiteCacheIsRefused is the reason the cache carries a MAC rather
// than an identifier.
//
// The file maps a site *name* to a site *id*. Anyone who can write it could
// repoint "mine.example" at someone else's id, and `statable now` would report
// on a property the user does not own, exit 0, and say nothing. A value
// derived from the key does not help: it is stored in the same file and can be
// copied across. A MAC cannot be produced without the key, so a rewritten file
// is simply a miss and the listing is fetched again.
func TestPoisonedSiteCacheIsRefused(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":7,"name":"mine.example","timezone":"UTC"}]}`)
			return
		}
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("site_id"))
		mu.Unlock()
		io.WriteString(w, `{"visitors":42}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{
		"STATABLE_CONFIG_DIR": dir,
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	run(t, env, "now")

	path := filepath.Join(dir, "sites-cache.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f siteCacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.MAC == "" {
		t.Fatal("the cache carries no MAC, so nothing authenticates it")
	}
	// The attacker keeps the signature, which is what an identifier would
	// have let them get away with, and repoints the name at another id.
	f.Sites[0].SiteID = 8
	b, _ = json.Marshal(f)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	run(t, env, "now")

	mu.Lock()
	defer mu.Unlock()
	for _, id := range asked {
		if id == "8" {
			t.Fatalf("a rewritten cache redirected the command to site %s; asked: %v", id, asked)
		}
	}
	if len(asked) != 2 {
		t.Fatalf("expected two current-visitor calls, got %v", asked)
	}
}

// TestNothingIsCachedIntoTheTempFallback: without HOME the config directory is
// a fixed path under the system temp directory, which anyone on the machine
// can create first and write to. A site listing read back from there maps a
// site name to an id someone else chose, so the cache stays out of it entirely
// and the listing is fetched every time.
func TestNothingIsCachedIntoTheTempFallback(t *testing.T) {
	var listings int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sites") {
			mu.Lock()
			listings++
			mu.Unlock()
			io.WriteString(w, `{"sites":[{"site_id":7,"name":"example.com","timezone":"UTC"}]}`)
			return
		}
		io.WriteString(w, `{"visitors":42}`)
	}))
	defer srv.Close()

	fallback := filepath.Join(os.TempDir(), noHomeDirName)
	env := map[string]string{
		"STATABLE_CONFIG_DIR": fallback,
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	t.Cleanup(func() { os.RemoveAll(fallback) })

	run(t, env, "now")
	run(t, env, "now")

	if _, err := os.Stat(filepath.Join(fallback, "sites-cache.json")); err == nil {
		t.Fatal("a cache was written into the world-writable fallback directory")
	}
	mu.Lock()
	defer mu.Unlock()
	if listings != 2 {
		t.Fatalf("sites listed %d times, want 2: something was cached in the fallback", listings)
	}
}

// TestCompletionNeverTouchesTheKeyring: a completion function runs on a
// keypress. On macOS a keyring read can raise a Keychain dialog; on Linux a
// locked gnome-keyring blocks for the full timeout. Either turns a TAB into a
// wait or an interruption, so the site completion resolves the key only from
// the flag, the environment and the plaintext file, and offers nothing when
// the only copy is in the keyring.
//
// The cache is warmed first. With it cold the read fails before the key is
// ever needed, and the test would pass whatever the completion did.
func TestCompletionNeverTouchesTheKeyring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":7,"name":"example.com","timezone":"UTC"}]}`)
			return
		}
		io.WriteString(w, `{"visitors":42}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{
		"STATABLE_CONFIG_DIR": dir,
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		auth.NoKeyringVar:     "1",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	run(t, env, "now")
	if _, err := os.Stat(filepath.Join(dir, "sites-cache.json")); err != nil {
		t.Fatalf("the cache was not warmed, so this test proves nothing: %v", err)
	}

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var outB strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(&outB, outR) }()
	go func() { defer wg.Done(); io.Copy(io.Discard, errR) }()

	for k, v := range env {
		t.Setenv(k, v)
	}
	root, rt := NewRoot(outW, errW)
	root.SetArgs([]string{"__complete", "--site", ""})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("completion failed: %v", err)
	}
	outW.Close()
	errW.Close()
	wg.Wait()

	// Proof the completion did reach the cache, so the assertion below is
	// about how it got the key rather than about it doing nothing.
	if !strings.Contains(outB.String(), "example.com") {
		t.Fatalf("completion offered nothing from a warm cache:\n%s", outB.String())
	}
	if rt.keyResolved() {
		t.Fatal("completing --site resolved the credential, which reaches the system keyring")
	}
}
