package cli

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSpec = `
openapi: 3.1.0
info: { title: Test, version: "1" }
paths:
  /sites:
    get:
      summary: List the sites a key can read
  /query:
    post:
      summary: Run one analytics query
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [metrics]
              properties:
                metrics: { type: array, items: { type: string } }
                site_id: { type: integer, description: Required on an all-sites key }
`

// checkServer answers /sites, /query and /openapi.yaml, and records the last
// query body so a test can assert what was sent.
type checkServer struct {
	srv   *httptest.Server
	value float64
	// nullMetric makes the server answer with a null metric, which is what a
	// rate does when it is undefined for the window. It is a separate flag
	// rather than a sentinel value, because a negative number is a perfectly
	// real metric and a test that conflates the two proves nothing.
	nullMetric bool
	lastQuery  map[string]any
	lastPath   string
}

func newCheckServer(t *testing.T, value float64) *checkServer {
	t.Helper()
	c := &checkServer{value: value}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.lastPath = r.URL.Path
		w.Header().Set("X-Request-ID", "ch-node01-check")
		switch {
		case strings.Contains(r.URL.Path, "openapi"):
			io.WriteString(w, testSpec)
		case strings.HasPrefix(r.URL.Path, "/sites"):
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
		case strings.HasPrefix(r.URL.Path, "/query"):
			body, _ := io.ReadAll(r.Body)
			c.lastQuery = map[string]any{}
			_ = json.Unmarshal(body, &c.lastQuery)
			if c.nullMetric {
				io.WriteString(w, `{"results":[{"metrics":{"visitors":null}}]}`)
				return
			}
			io.WriteString(w, `{"results":[{"metrics":{"visitors":`+trimFloat(c.value)+`}}]}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"code":"not_found","error":"no"}`)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *checkServer) env(t *testing.T) map[string]string {
	e := emptyConfig(t)
	e["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	e["STATABLE_API_URL"] = c.srv.URL
	return e
}

// TestCheckExitCodeIsTheAnswer. The third exit code is the whole point of the
// command: a pipeline has to tell "traffic dropped" apart from "the tool is
// broken", which one shared failure code makes impossible.
func TestCheckExitCodeIsTheAnswer(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		args  []string
		want  int
	}{
		{"above the minimum", 1204, []string{"--min", "100"}, 0},
		{"below the minimum", 12, []string{"--min", "100"}, 3},
		{"exactly the minimum", 100, []string{"--min", "100"}, 0},
		{"under the maximum", 12, []string{"--max", "100"}, 0},
		{"over the maximum", 1204, []string{"--max", "100"}, 3},
		{"inside a range", 50, []string{"--min", "10", "--max", "100"}, 0},
		{"outside a range", 500, []string{"--min", "10", "--max", "100"}, 3},
		{"no bound given", 50, nil, 64},
		{"inverted bounds", 50, []string{"--min", "100", "--max", "10"}, 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCheckServer(t, tc.value)
			args := append([]string{"check", "--metric", "visitors"}, tc.args...)
			_, _, code := run(t, s.env(t), append(args, "--json")...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestCheckExitCodeIsFormatIndependent: the verdict cannot depend on how it
// is printed.
func TestCheckExitCodeIsFormatIndependent(t *testing.T) {
	s := newCheckServer(t, 12)
	var codes []int
	for _, f := range []string{"human", "json", "csv"} {
		_, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "100", "--format", f)
		codes = append(codes, code)
	}
	for _, c := range codes {
		if c != codes[0] {
			t.Fatalf("exit codes differ by format: %v", codes)
		}
	}
	if codes[0] != 3 {
		t.Fatalf("a failed threshold must exit 3, got %d", codes[0])
	}
}

// TestCheckReportsTheNumberItJudged: a bare exit code leaves the operator
// unable to see how far off the value was.
func TestCheckReportsTheNumberItJudged(t *testing.T) {
	s := newCheckServer(t, 12)
	out, _, _ := run(t, s.env(t), "check", "--metric", "visitors", "--min", "100", "--json")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out)
	}
	if v["value"] != float64(12) || v["min"] != float64(100) || v["ok"] != false {
		t.Fatalf("report = %v", v)
	}
	if v["metric"] != "visitors" {
		t.Fatalf("the metric must be named: %v", v)
	}
}

// TestCheckRefusesToJudgeAnUndefinedMetric: a rate can be null for a window,
// and treating that as zero would silently pass or fail a check on a number
// that does not exist.
func TestCheckRefusesToJudgeAnUndefinedMetric(t *testing.T) {
	s := newCheckServer(t, 0)
	s.nullMetric = true
	_, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "1", "--json")
	if code == 0 || code == 3 {
		t.Fatalf("exit = %d: an undefined metric is neither a pass nor a fail", code)
	}
}

func TestAPICallSendsVarsWithTheRightTypes(t *testing.T) {
	s := newCheckServer(t, 1)
	_, _, code := run(t, s.env(t), "api", "call", "POST", "/query",
		"--var", "site_id=1",
		"--var", "limit=25",
		"--var", `metrics=["visitors"]`,
		"--var", "date_range=7d",
		"--raw-var", "page=25")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	q := s.lastQuery
	if _, ok := q["limit"].(float64); !ok {
		t.Fatalf("--var limit=25 should send a number, got %T", q["limit"])
	}
	if _, ok := q["metrics"].([]any); !ok {
		t.Fatalf("--var metrics=[...] should send an array, got %T", q["metrics"])
	}
	if q["date_range"] != "7d" {
		t.Fatalf("a bare word should send as a string, got %v", q["date_range"])
	}
	// This is the ambiguity the two flags exist to remove.
	if q["page"] != "25" {
		t.Fatalf("--raw-var must always send a string, got %T %v", q["page"], q["page"])
	}
}

// TestAPIAllowErrorsSeparatesTransportFromOperation.
func TestAPIAllowErrorsSeparatesTransportFromOperation(t *testing.T) {
	s := newCheckServer(t, 1)

	out, _, code := run(t, s.env(t), "api", "call", "GET", "/nope", "--allow-errors")
	if code == 0 {
		t.Fatal("a 404 is still a failed operation, so the code must say so")
	}
	if !strings.Contains(out, "not_found") {
		t.Fatalf("--allow-errors must print the body, got %q", out)
	}

	outNo, _, codeNo := run(t, s.env(t), "api", "call", "GET", "/nope")
	if codeNo == 0 {
		t.Fatal("without --allow-errors a 404 must still fail")
	}
	if strings.Contains(outNo, "not_found") {
		t.Fatalf("without --allow-errors the raw body must not be printed: %q", outNo)
	}
}

// TestAPIPassesTheBodyThroughUntouched: the escape hatch must not reformat
// what the server said.
func TestAPIPassesTheBodyThroughUntouched(t *testing.T) {
	s := newCheckServer(t, 1)
	out, _, code := run(t, s.env(t), "api", "call", "GET", "/sites")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), `{"sites"`) {
		t.Fatalf("the body was reformatted: %q", out)
	}
}

func TestAPISearchAndDescribe(t *testing.T) {
	s := newCheckServer(t, 1)

	out, _, code := run(t, s.env(t), "api", "search", "analytics", "--format", "csv")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "/query") {
		t.Fatalf("search missed the matching endpoint: %q", out)
	}

	d, _, code := run(t, s.env(t), "api", "describe", "/query", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var op map[string]any
	if err := json.Unmarshal([]byte(d), &op); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, d)
	}
	if op["method"] != "POST" || op["path"] != "/query" {
		t.Fatalf("operation = %v", op)
	}
	fields, _ := op["body_fields"].([]any)
	if len(fields) == 0 {
		t.Fatalf("the request body must be described: %v", op)
	}
	first, _ := fields[0].(map[string]any)
	if first["required"] != true {
		t.Fatalf("required fields come first: %v", fields)
	}
}

// TestAPIDescribeUnknownEndpointSuggests: telling a caller only that it does
// not exist leaves them with nowhere to go.
func TestAPIDescribeUnknownEndpointSuggests(t *testing.T) {
	s := newCheckServer(t, 1)
	out, _, code := run(t, s.env(t), "api", "describe", "/nope", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1: naming a real endpoint fixes this", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if steps, _ := v["next_steps"].([]any); len(steps) == 0 {
		t.Fatalf("the caller must be told how to find one: %v", v)
	}
}

// TestSpecIsCachedNotRefetched: the document changes with a release, not with
// a request, and refetching it would spend the caller's hourly budget on
// documentation.
func TestSpecIsCachedNotRefetched(t *testing.T) {
	s := newCheckServer(t, 1)
	env := s.env(t)

	if _, _, code := run(t, env, "api", "search", "query", "--format", "csv"); code != 0 {
		t.Fatal("first search failed")
	}
	s.lastPath = ""
	if _, _, code := run(t, env, "api", "search", "sites", "--format", "csv"); code != 0 {
		t.Fatal("second search failed")
	}
	if strings.Contains(s.lastPath, "openapi") {
		t.Fatal("the description was fetched again instead of being read from the cache")
	}
}

// TestCheckRefusesANonFiniteBound. A NaN bound makes every comparison false,
// so the check would pass whatever the number was: a deploy gate that can
// never fail is worse than no gate at all.
func TestCheckRefusesANonFiniteBound(t *testing.T) {
	s := newCheckServer(t, 1)
	for _, bound := range []string{"NaN", "Inf", "-Inf"} {
		t.Run(bound, func(t *testing.T) {
			_, errOut, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", bound)
			if code != 64 {
				t.Fatalf("exit = %d, want 64: %s is not a bound", code, bound)
			}
			if !strings.Contains(errOut, "real number") {
				t.Fatalf("the message should say why, got %q", errOut)
			}
		})
	}
	// A real bound still works.
	if _, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "0"); code != 0 {
		t.Fatalf("a finite bound must still be accepted, exit = %d", code)
	}
}

// TestCheckBoundaryIsInclusive: a value exactly on the bound passes, which is
// what "at least 100" means to the person writing the gate.
func TestCheckBoundaryIsInclusive(t *testing.T) {
	s := newCheckServer(t, 100)
	if _, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "100"); code != 0 {
		t.Fatalf("exactly the minimum must pass, exit = %d", code)
	}
	if _, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--max", "100"); code != 0 {
		t.Fatalf("exactly the maximum must pass, exit = %d", code)
	}
}

// TestCheckMinZeroIsABound: zero is a real threshold, and "no bound given" and
// "the bound is zero" cannot be the same thing.
func TestCheckMinZeroIsABound(t *testing.T) {
	s := newCheckServer(t, -5)
	out, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "0", "--json")
	if code != 3 {
		t.Fatalf("exit = %d, want 3: -5 is below zero", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v["min"] != float64(0) {
		t.Fatalf("the bound must be reported: %v", v)
	}
}

// TestAPIRefusesAHeaderItCannotSend: the transport rejects a line break with a
// generic failure that reads as "the network is down".
func TestAPIRefusesAHeaderItCannotSend(t *testing.T) {
	s := newCheckServer(t, 1)
	for _, h := range []string{"X-A: one\ntwo", "X-A: one\rtwo", "Bad Name: v", ": v", "noColon"} {
		t.Run(h, func(t *testing.T) {
			_, errOut, code := run(t, s.env(t), "api", "call", "GET", "/sites", "-H", h)
			if code != 64 {
				t.Fatalf("exit = %d, want 64 for a header that cannot be sent", code)
			}
			if strings.Contains(errOut, "could not reach") {
				t.Fatalf("a malformed header must not be reported as a network failure: %q", errOut)
			}
		})
	}
}

// TestAPICannotOverrideAuthorization: the escape hatch sends the user's
// credential, and a header flag must not be able to redirect or replace it.
func TestAPICannotOverrideAuthorization(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_real_key_long_enough"
	env["STATABLE_API_URL"] = srv.URL

	for _, h := range []string{"Authorization: Bearer stolen", "authorization: Bearer stolen", "AUTHORIZATION: x"} {
		if _, _, code := run(t, env, "api", "call", "GET", "/x", "-H", h); code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if got != "Bearer stbl_real_key_long_enough" {
			t.Fatalf("%q replaced the credential: %q", h, got)
		}
	}
}

// TestGETVarsAreJSONNotGoSyntax. %v prints Go syntax: an array became
// "[a b]", an object "map[a:1]" and null "<nil>", silently and at exit 0, so
// every GET endpoint taking a structured parameter was unreachable.
func TestGETVarsAreJSONNotGoSyntax(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	if _, _, code := run(t, env, "api", "call", "GET", "/x",
		"--var", `list=["a","b"]`, "--var", `obj={"a":1}`,
		"--var", "n=null", "--var", "s=7d", "--var", "num=25"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if got := vals.Get("list"); got != `["a","b"]` {
		t.Fatalf("list = %q, want JSON", got)
	}
	if got := vals.Get("obj"); got != `{"a":1}` {
		t.Fatalf("obj = %q, want JSON", got)
	}
	if got := vals.Get("n"); got != "null" {
		t.Fatalf("null = %q", got)
	}
	// A bare word stays itself: date_range=7d must not become "7d" quoted.
	if got := vals.Get("s"); got != "7d" {
		t.Fatalf("string = %q", got)
	}
	if got := vals.Get("num"); got != "25" {
		t.Fatalf("number = %q", got)
	}
}

// TestAllowErrorsKeepsTheClassifiedError. The flag CI and agents reach for
// must not flatten a recoverable failure into a generic one: a script that
// retries on 1 and pages a human on 2 would page a human for an expired key.
func TestAllowErrorsKeepsTheClassifiedError(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
	}{
		{"unauthorized", 401, `{"code":"unauthorized","error":"bad key"}`, 1},
		{"rate limited", 429, `{"code":"rate_limited","error":"slow"}`, 1},
		{"bad request", 400, `{"code":"unknown_metric","error":"no"}`, 2},
		{"server error", 500, `{}`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-ID", "rq-1")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			env := emptyConfig(t)
			env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
			env["STATABLE_API_URL"] = srv.URL

			out, _, code := run(t, env, "api", "call", "GET", "/x", "--allow-errors")
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d: --allow-errors must not change the classification",
					code, tc.wantCode)
			}
			if !strings.Contains(out, strings.Split(tc.body, `"`)[0]) && tc.body != `{}` {
				t.Fatalf("the body must still be printed, got %q", out)
			}
		})
	}
}

// TestDescribeHonoursCSV: asking for CSV and receiving JSON is the contract
// break Record exists to prevent, and describe was the one command left doing
// it.
func TestDescribeHonoursCSV(t *testing.T) {
	s := newCheckServer(t, 1)
	out, _, code := run(t, s.env(t), "api", "describe", "/query", "--format", "csv")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("--format csv produced JSON: %q", out)
	}
	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v (%q)", err, out)
	}
	if recs[0][0] != "name" || len(recs) < 2 {
		t.Fatalf("want a parameter table, got %v", recs)
	}
}

func TestRepeatedHeadersAreAllSent(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("Cookie")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	if _, _, code := run(t, env, "api", "call", "GET", "/x", "-H", "Cookie: a=1", "-H", "Cookie: b=2"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(got) != 2 {
		t.Fatalf("headers = %v; a repeated header must not be collapsed", got)
	}
}

// TestVarAndRawVarCannotClash: these two flags exist to remove the ambiguity
// about a value's type, and letting one overwrite the other puts it back.
func TestVarAndRawVarCannotClash(t *testing.T) {
	s := newCheckServer(t, 1)
	_, errOut, code := run(t, s.env(t), "api", "call", "POST", "/query", "--var", "k=1", "--raw-var", "k=2")
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(errOut, "both") {
		t.Fatalf("the message should name the conflict, got %q", errOut)
	}
}

func TestAPIBodyAcceptsAnyJSONValue(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	// An endpoint taking an array body is unreachable if only objects are
	// accepted, and the command promises every endpoint is reachable.
	if _, _, code := run(t, env, "api", "call", "POST", "/x", "--body", `[1,2,3]`); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got != `[1,2,3]` {
		t.Fatalf("body = %q", got)
	}
}

func TestBadMethodIsAUsageError(t *testing.T) {
	s := newCheckServer(t, 1)
	_, errOut, code := run(t, s.env(t), "api", "call", "FOO", "/x")
	if code != 64 {
		t.Fatalf("exit = %d, want 64: nothing was sent", code)
	}
	if !strings.Contains(errOut, "FOO") {
		t.Fatalf("the message should name the bad method, got %q", errOut)
	}
}

// TestCheckShowsTheBoundOnAPass: a CI log that says only "within bounds"
// cannot answer "what gate did this clear?".
func TestCheckShowsTheBoundOnAPass(t *testing.T) {
	s := newCheckServer(t, 1204)
	out, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "100")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "100") {
		t.Fatalf("the bound must be visible on a pass, got %q", out)
	}
}

// TestCheckDoesNotOfferAComparisonItIgnores.
func TestCheckDoesNotOfferAComparisonItIgnores(t *testing.T) {
	s := newCheckServer(t, 1)
	_, _, code := run(t, s.env(t), "check", "--metric", "visitors", "--min", "1", "--compare", "previous")
	if code != 64 {
		t.Fatalf("exit = %d, want 64: a flag the command ignores must not be accepted", code)
	}
}

// TestVerboseSurvivesQuiet: --quiet silencing something --verbose explicitly
// asked for makes the two flags cancel out with no explanation.
func TestVerboseSurvivesQuiet(t *testing.T) {
	s := newCheckServer(t, 1)
	_, errOut, _ := run(t, s.env(t), "api", "call", "GET", "/sites", "--verbose", "--quiet")
	if !strings.Contains(errOut, "request id") {
		t.Fatalf("--verbose must still report with --quiet, got %q", errOut)
	}
}

// TestSpecCacheIsKeyedByHost: one shared filename served a staging
// description for production, and the wrong parameter set for describe.
func TestSpecCacheIsKeyedByHost(t *testing.T) {
	a := newCheckServer(t, 1)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openapi") {
			io.WriteString(w, "paths: {/only-on-b: {get: {summary: b only}}}")
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer b.Close()

	env := a.env(t)
	if _, _, code := run(t, env, "api", "search", "--format", "csv"); code != 0 {
		t.Fatal("first host failed")
	}

	envB := map[string]string{}
	for k, v := range env {
		envB[k] = v
	}
	envB["STATABLE_API_URL"] = b.URL
	out, _, code := run(t, envB, "api", "search", "--format", "csv")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "only-on-b") {
		t.Fatalf("the second host got the first host's description: %q", out)
	}
}

// TestFutureCacheTimestampDoesNotPinForever: time.Since is negative for a
// future mtime, and negative < 24h, so the file was never stale again.
func TestFutureCacheTimestampDoesNotPinForever(t *testing.T) {
	s := newCheckServer(t, 1)
	env := s.env(t)
	if _, _, code := run(t, env, "api", "search", "--format", "csv"); code != 0 {
		t.Fatal("warm-up failed")
	}

	dir := env["STATABLE_CONFIG_DIR"]
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var cached string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "openapi-") {
			cached = filepath.Join(dir, e.Name())
		}
	}
	if cached == "" {
		t.Fatal("no cache file was written")
	}
	future := time.Now().Add(365 * 24 * time.Hour)
	if err := os.Chtimes(cached, future, future); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("paths: {/stale: {get: {summary: stale}}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cached, future, future); err != nil {
		t.Fatal(err)
	}

	out, _, _ := run(t, env, "api", "search", "--format", "csv")
	if strings.Contains(out, "/stale") {
		t.Fatalf("a cache dated in the future was treated as fresh: %q", out)
	}
}
