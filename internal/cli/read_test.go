package cli

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fakeAPI answers the endpoints the reading commands use. lastQuery records
// the body of the most recent /query so a test can assert what was sent, not
// only what came back.
type fakeAPI struct {
	srv       *httptest.Server
	lastQuery map[string]any
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "ch-node01-fake")
		w.Header().Set("X-RateLimit-Limit", "1200")
		w.Header().Set("X-RateLimit-Remaining", "1180")
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasPrefix(r.URL.Path, "/sites"):
			io.WriteString(w, `{"sites":[
				{"site_id":3093477,"name":"example.com","timezone":"Europe/Amsterdam","hash":"h1","stats_start_date":"2026-04-07","created_at":"2026-02-21T12:52:54Z"},
				{"site_id":4000001,"name":"https://shop.example/","timezone":"UTC","hash":"h2","stats_start_date":null,"created_at":"2026-03-01T09:00:00Z"}]}`)

		case strings.HasPrefix(r.URL.Path, "/current-visitors"):
			io.WriteString(w, `{"site_id":3093477,"visitors":42}`)

		case strings.HasPrefix(r.URL.Path, "/query"):
			body, _ := io.ReadAll(r.Body)
			f.lastQuery = map[string]any{}
			_ = json.Unmarshal(body, &f.lastQuery)

			dims, _ := f.lastQuery["dimensions"].([]any)
			echo := `"query":{"date_range":["2026-09-02","2026-09-08"]}`
			switch {
			case len(dims) == 0:
				io.WriteString(w, `{"results":[{"metrics":{"visitors":1204,"pageviews":5678,"bounce_rate":41.2,"visit_duration":161}}],`+echo+`}`)
			case strings.HasPrefix(dims[0].(string), "time"):
				d := dims[0].(string)
				io.WriteString(w, `{"results":[
					{"dimensions":{"`+d+`":"2026-09-02"},"metrics":{"visitors":210}},
					{"dimensions":{"`+d+`":"2026-09-03"},"metrics":{"visitors":0}},
					{"dimensions":{"`+d+`":"2026-09-04"},"metrics":{"visitors":588}}],`+echo+`}`)
			case dims[0].(string) == "visit:country":
				io.WriteString(w, `{"results":[
					{"dimensions":{"visit:country":"US"},"labels":{"visit:country":"United States"},"metrics":{"visitors":500}},
					{"dimensions":{"visit:country":"DE"},"labels":{"visit:country":"Germany"},"metrics":{"visitors":180}}],
					"meta":{"total":57,"limit":10,"offset":0,"has_more":true},`+echo+`}`)
			default:
				d := dims[0].(string)
				io.WriteString(w, `{"results":[
					{"dimensions":{"`+d+`":"/"},"metrics":{"visitors":1204,"pageviews":5678}},
					{"dimensions":{"`+d+`":"/pricing"},"metrics":{"visitors":87,"pageviews":120}}],
					"meta":{"total":57,"limit":10,"offset":0,"has_more":false},`+echo+`}`)
			}
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"code":"not_found","error":"no such path"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) env(t *testing.T) map[string]string {
	e := emptyConfig(t)
	e["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	e["STATABLE_API_URL"] = f.srv.URL
	return e
}

func TestSitesLists(t *testing.T) {
	f := newFakeAPI(t)
	out, _, code := run(t, f.env(t), "sites", "--format", "csv")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v (%q)", err, out)
	}
	if len(recs) != 3 {
		t.Fatalf("want a header and two sites, got %v", recs)
	}
	if recs[1][0] != "3093477" {
		t.Fatalf("first column should be the id, got %v", recs[1])
	}
}

// TestSeveralSitesMustBeNamed: guessing which site to report on is worse than
// stopping, and a script has to be told which flag settles it.
func TestSeveralSitesMustBeNamed(t *testing.T) {
	f := newFakeAPI(t)
	out, _, code := run(t, f.env(t), "stats", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1: naming a site is an action the caller can take", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output does not parse: %v (%q)", err, out)
	}
	if v["status"] != "action_required" {
		t.Fatalf("status = %v", v["status"])
	}
	steps, _ := v["next_steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("the error must name how to choose a site")
	}
}

func TestSiteFlagSelectsByHostname(t *testing.T) {
	f := newFakeAPI(t)
	// The site is stored as a full URL; a person types the bare hostname.
	_, _, code := run(t, f.env(t), "now", "--site", "shop.example", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want a hostname to match a URL-shaped name", code)
	}
}

func TestNowIsOneNumber(t *testing.T) {
	f := newFakeAPI(t)
	out, _, code := run(t, f.env(t), "now", "--site", "example.com")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("output = %q, want a bare number", out)
	}
}

func TestStatsSendsWhatWasAsked(t *testing.T) {
	f := newFakeAPI(t)
	_, _, code := run(t, f.env(t), "stats", "--site", "example.com",
		"--range", "30d", "--filter", "country=US,DE", "--filter", "page~/blog", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if f.lastQuery["date_range"] != "30d" {
		t.Fatalf("date_range = %v", f.lastQuery["date_range"])
	}
	filters, _ := f.lastQuery["filters"].([]any)
	if len(filters) != 2 {
		t.Fatalf("separate --filter flags must stay separate (AND), got %v", filters)
	}
	first, _ := filters[0].(map[string]any)
	vals, _ := first["values"].([]any)
	if len(vals) != 2 {
		t.Fatalf("comma-separated values must become one OR'd filter, got %v", first)
	}
	if first["operator"] != "is" {
		t.Fatalf("operator = %v", first["operator"])
	}
	second, _ := filters[1].(map[string]any)
	if second["operator"] != "contains" {
		t.Fatalf("~ must become contains, got %v", second["operator"])
	}
}

// TestValidationHappensBeforeTheNetwork: a misspelled metric used to be
// reported as "name a site", because listing sites ran first.
func TestValidationHappensBeforeTheNetwork(t *testing.T) {
	f := newFakeAPI(t)
	out, _, _ := run(t, f.env(t), "stats", "--metric", "clicks", "--json")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output does not parse: %v (%q)", err, out)
	}
	issues, _ := v["issues"].([]any)
	if len(issues) == 0 {
		t.Fatalf("no issue reported: %v", v)
	}
	first, _ := issues[0].(map[string]any)
	if first["code"] != "UNKNOWN_METRIC" {
		t.Fatalf("code = %v, want the local validation to fire first", first["code"])
	}
	if f.lastQuery != nil {
		t.Fatal("an invalid query must never reach the server")
	}
}

func TestSeriesHumanOutputIsPipeSafe(t *testing.T) {
	f := newFakeAPI(t)
	out, _, code := run(t, f.env(t), "series", "--site", "example.com", "--range", "7d")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("line %d is not two columns: %q", i, line)
		}
		if !strings.HasPrefix(fields[0], "2026-") {
			t.Fatalf("line %d does not start with a bucket: %q", i, line)
		}
	}
	// A sparkline is a reading aid for a terminal; in a pipe it would be a
	// last line no parser expects.
	if strings.ContainsAny(out, "▁▂▃▄▅▆▇█") {
		t.Fatalf("a sparkline reached a pipe: %q", out)
	}
}

// TestGeoBreakdownGivesCodesToMachinesAndNamesToPeople: the code is what the
// matching filter accepts, so a script needs it; a person needs the name.
func TestGeoBreakdownGivesCodesToMachinesAndNamesToPeople(t *testing.T) {
	f := newFakeAPI(t)

	human, _, _ := run(t, f.env(t), "top", "countries", "--site", "example.com")
	if !strings.Contains(human, "United States") {
		t.Fatalf("human output should show the name, got %q", human)
	}

	machine, _, _ := run(t, f.env(t), "top", "countries", "--site", "example.com", "--format", "csv")
	recs, err := csv.NewReader(strings.NewReader(machine)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v (%q)", err, machine)
	}
	if recs[1][0] != "US" {
		t.Fatalf("machine output should carry the code, got %v", recs[1])
	}
	if recs[0][len(recs[0])-1] != "label" {
		t.Fatalf("machine output should also carry the readable label, got %v", recs[0])
	}
}

func TestTopPagingIsAnnouncedOnStderr(t *testing.T) {
	f := newFakeAPI(t)
	out, errOut, _ := run(t, f.env(t), "top", "countries", "--site", "example.com", "--format", "csv")
	if !strings.Contains(errOut, "--offset") {
		t.Fatalf("more rows exist, so the next page should be named: %q", errOut)
	}
	if strings.Contains(out, "--offset") {
		t.Fatalf("the hint must not reach stdout: %q", out)
	}
}

func TestTopWithoutAnArgumentIsUsage(t *testing.T) {
	f := newFakeAPI(t)
	_, errOut, code := run(t, f.env(t), "top")
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(errOut, "pages") {
		t.Fatalf("the message should list the short names, got %q", errOut)
	}
}

func TestQueryBreakdownSendsLimitAndOffset(t *testing.T) {
	f := newFakeAPI(t)
	_, _, code := run(t, f.env(t), "query", "--site", "example.com",
		"--metric", "visitors", "--dimension", "event:page", "--limit", "25", "--offset", "50", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if f.lastQuery["limit"] != float64(25) || f.lastQuery["offset"] != float64(50) {
		t.Fatalf("limit/offset not sent: %v", f.lastQuery)
	}
}

// TestQueryTotalsNeverSendLimit: the API rejects limit on an aggregate, so it
// must be absent rather than defaulted.
func TestQueryTotalsNeverSendLimit(t *testing.T) {
	f := newFakeAPI(t)
	_, _, code := run(t, f.env(t), "query", "--site", "example.com", "--metric", "visitors", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if _, present := f.lastQuery["limit"]; present {
		t.Fatalf("limit must be absent on totals, got %v", f.lastQuery)
	}
}

func TestCompareIsForwarded(t *testing.T) {
	f := newFakeAPI(t)
	if _, _, code := run(t, f.env(t), "stats", "--site", "example.com", "--compare", "previous", "--json"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if f.lastQuery["compare"] != "previous_period" {
		t.Fatalf("compare = %v", f.lastQuery["compare"])
	}
}

// TestEveryReadingCommandHonoursEveryFormat guards the whole surface at once.
func TestEveryReadingCommandHonoursEveryFormat(t *testing.T) {
	f := newFakeAPI(t)
	cmds := [][]string{
		{"sites"},
		{"now", "--site", "example.com"},
		{"stats", "--site", "example.com"},
		{"series", "--site", "example.com"},
		{"top", "pages", "--site", "example.com"},
		{"query", "--site", "example.com", "--metric", "visitors"},
	}
	for _, base := range cmds {
		for _, format := range []string{"human", "json", "csv"} {
			t.Run(strings.Join(base, " ")+"/"+format, func(t *testing.T) {
				out, _, code := run(t, f.env(t), append(append([]string{}, base...), "--format", format)...)
				if code != 0 {
					t.Fatalf("exit = %d", code)
				}
				if strings.TrimSpace(out) == "" {
					t.Fatal("no output")
				}
				switch format {
				case "json":
					if err := json.NewDecoder(strings.NewReader(out)).Decode(new(any)); err != nil {
						t.Fatalf("not JSON: %v (%q)", err, out)
					}
				case "csv":
					if strings.HasPrefix(strings.TrimSpace(out), "{") {
						t.Fatalf("CSV was asked for and JSON came back: %q", out)
					}
					if _, err := csv.NewReader(strings.NewReader(out)).ReadAll(); err != nil {
						t.Fatalf("not CSV: %v (%q)", err, out)
					}
				}
			})
		}
	}
}

// TestStatsWithNoUsableMetricsIsNotSilentSuccess: exiting 0 with no output
// looks like success with no answer. If not one requested metric came back,
// the command has to say so.
func TestStatsWithNoUsableMetricsIsNotSilentSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		io.WriteString(w, `{"results":[{}]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	out, _, code := run(t, env, "stats", "--json")
	if code == 0 {
		t.Fatalf("exit 0 with nothing to show is indistinguishable from success: %q", out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output does not parse: %v (%q)", err, out)
	}
	if v["status"] == nil {
		t.Fatalf("no envelope: %v", v)
	}
}

// TestReadingCommandsEmitRealNumbers: every command whose JSON a script would
// do arithmetic on.
func TestReadingCommandsEmitRealNumbers(t *testing.T) {
	f := newFakeAPI(t)
	cases := []struct {
		args  []string
		field string
	}{
		{[]string{"sites"}, "site_id"},
		{[]string{"series", "--site", "example.com"}, "visitors"},
		{[]string{"top", "pages", "--site", "example.com"}, "visitors"},
		{[]string{"query", "--site", "example.com", "--metric", "visitors", "--dimension", "event:page"}, "visitors"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			out, _, code := run(t, f.env(t), append(tc.args, "--json")...)
			if code != 0 {
				t.Fatalf("exit = %d", code)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("not a JSON array: %v (%q)", err, out)
			}
			if len(rows) == 0 {
				t.Fatal("no rows")
			}
			if _, ok := rows[0][tc.field].(float64); !ok {
				t.Fatalf("%s = %T (%v), want a number a caller can add to",
					tc.field, rows[0][tc.field], rows[0][tc.field])
			}
		})
	}
}

// TestTimeAliasKeepsBucketLabels: query.md documents `time` as an alias of
// `time:day`, so a request for one may be answered with the other. Reading a
// single hard-coded key left every date blank, in all three formats, at exit 0.
func TestTimeAliasKeepsBucketLabels(t *testing.T) {
	for _, serverKey := range []string{"time", "time:day"} {
		for _, asked := range []string{"time", "time:day"} {
			t.Run(asked+"/"+serverKey, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/sites") {
						io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
						return
					}
					io.WriteString(w, `{"results":[{"dimensions":{"`+serverKey+`":"2026-01-01"},"metrics":{"visitors":5}}]}`)
				}))
				defer srv.Close()

				env := emptyConfig(t)
				env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
				env["STATABLE_API_URL"] = srv.URL

				out, _, code := run(t, env, "query", "--metric", "visitors",
					"--dimension", asked, "--json")
				if code != 0 {
					t.Fatalf("exit = %d", code)
				}
				var rows []map[string]any
				if err := json.Unmarshal([]byte(out), &rows); err != nil {
					t.Fatal(err)
				}
				if rows[0]["time"] != "2026-01-01" {
					t.Fatalf("bucket label lost: %v", rows[0])
				}
			})
		}
	}
}

// TestCompareReachesMachineOutput: the compare is requested and paid for, and
// query.md calls the per-row case the valuable one. Keeping it in the human
// string alone threw that away for every scripted caller.
func TestCompareReachesMachineOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"dimensions"`) {
			io.WriteString(w, `{"results":[{"dimensions":{"event:page":"/"},"metrics":{"visitors":10},
				"compare":{"visitors":{"value":5,"change":100}}}]}`)
			return
		}
		io.WriteString(w, `{"results":[{"metrics":{"visitors":10},"compare":{"visitors":{"value":5,"change":100}}}]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	t.Run("totals", func(t *testing.T) {
		out, _, _ := run(t, env, "stats", "--metric", "visitors", "--compare", "previous", "--json")
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatal(err)
		}
		if v["visitors_previous"] != float64(5) || v["visitors_change"] != float64(100) {
			t.Fatalf("compare missing from totals: %v", v)
		}
	})

	t.Run("breakdown", func(t *testing.T) {
		out, _, _ := run(t, env, "top", "pages", "--metric", "visitors", "--compare", "previous", "--json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		if rows[0]["visitors_previous"] != float64(5) || rows[0]["visitors_change"] != float64(100) {
			t.Fatalf("compare missing from a breakdown row: %v", rows[0])
		}
	})

	t.Run("no compare asked, no columns", func(t *testing.T) {
		out, _, _ := run(t, env, "top", "pages", "--metric", "visitors", "--json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		if _, present := rows[0]["visitors_previous"]; present {
			t.Fatalf("compare columns must not appear unasked: %v", rows[0])
		}
	})
}

// TestDimensionKeyIsStableAcrossCommands: two commands issuing the identical
// request must return the same shape, or no script can write one extractor.
func TestDimensionKeyIsStableAcrossCommands(t *testing.T) {
	f := newFakeAPI(t)

	keys := func(args ...string) []string {
		out, _, code := run(t, f.env(t), append(args, "--json")...)
		if code != 0 {
			t.Fatalf("%v: exit = %d (%q)", args, code, out)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		ks := make([]string, 0, len(rows[0]))
		for k := range rows[0] {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}

	viaTop := keys("top", "countries", "--site", "example.com")
	viaFull := keys("top", "visit:country", "--site", "example.com")
	viaQuery := keys("query", "-d", "visit:country", "-m", "visitors", "--site", "example.com")

	if strings.Join(viaTop, ",") != strings.Join(viaQuery, ",") {
		t.Fatalf("top and query disagree: %v vs %v", viaTop, viaQuery)
	}
	if strings.Join(viaTop, ",") != strings.Join(viaFull, ",") {
		t.Fatalf("an alias and the full dimension disagree: %v vs %v", viaTop, viaFull)
	}
	// The readable name has to be reachable from every command.
	found := false
	for _, k := range viaQuery {
		if k == "label" {
			found = true
		}
	}
	if !found {
		t.Fatalf("query lost the geo label: %v", viaQuery)
	}
}

// TestSiteNamedLikeAnIDIsReachable: with the numeric lookup first, a site
// literally named "12345" was unreachable by name and the user silently got
// the analytics of whichever site happened to have that id.
func TestSiteNamedLikeAnIDIsReachable(t *testing.T) {
	var sentSiteID float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[
				{"site_id":999,"name":"12345","timezone":"UTC","hash":"a","stats_start_date":null,"created_at":"x"},
				{"site_id":12345,"name":"example.com","timezone":"UTC","hash":"b","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		sentSiteID, _ = req["site_id"].(float64)
		io.WriteString(w, `{"results":[{"metrics":{"visitors":1}}]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	if _, _, code := run(t, env, "stats", "--site", "12345", "--metric", "visitors", "--json"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if sentSiteID != 999 {
		t.Fatalf("site_id = %v; a name must win over a coincidental id", sentSiteID)
	}
}

// TestQueryWithNoTotalsIsNotAnEmptyCSVRow: an empty record renders as two bare
// newlines, which a CSV reader accepts as one empty row and a caller reads as
// a real answer.
func TestQueryWithNoTotalsIsNotAnEmptyCSVRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	out, _, code := run(t, env, "query", "--metric", "visitors", "--format", "csv")
	if code == 0 {
		t.Fatalf("exit 0 with %q is indistinguishable from a real empty answer", out)
	}
}

// TestCorruptSettingsAreReported: the same rule the project file follows. A
// vanished default with no explanation sends the user to re-run a command
// they already ran.
func TestCorruptSettingsAreReported(t *testing.T) {
	f := newFakeAPI(t)
	env := f.env(t)
	if err := os.WriteFile(filepath.Join(env["STATABLE_CONFIG_DIR"], "settings.json"),
		[]byte(`{"default_site": "example.com"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, _ := run(t, env, "sites", "--format", "csv")
	if !strings.Contains(errOut, "could not be parsed") {
		t.Fatalf("a corrupt settings file must be reported, got %q", errOut)
	}
}

// TestSitesUseKeepsUnknownKeys: rewriting the file down to what this build
// recognises silently deletes a setting written by a newer one.
func TestSitesUseKeepsUnknownKeys(t *testing.T) {
	f := newFakeAPI(t)
	env := f.env(t)
	path := filepath.Join(env["STATABLE_CONFIG_DIR"], "settings.json")
	if err := os.WriteFile(path, []byte(`{"default_site":"old.example","future_key":42}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, code := run(t, env, "sites", "use", "example.com", "--json"); code != 0 {
		t.Fatalf("exit = %d", code)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["default_site"] != "example.com" {
		t.Fatalf("default not updated: %v", m)
	}
	if m["future_key"] != float64(42) {
		t.Fatalf("an unknown key was dropped: %v", m)
	}
}

// TestSitesUseDoesNotBlameAMissingProjectFile: --site overwrites the same
// field a project file sets, so warning about a file that does not exist
// sends the user looking for it.
func TestSitesUseDoesNotBlameAMissingProjectFile(t *testing.T) {
	f := newFakeAPI(t)
	_, errOut, _ := run(t, f.env(t), "sites", "use", "example.com", "--site", "shop.example", "--json")
	if strings.Contains(errOut, "project file") || strings.Contains(errOut, ".statable.yml") {
		t.Fatalf("no project file exists, yet one was blamed: %q", errOut)
	}
}

// TestTopKeepsTimeBucketLabels: `top` supports a time dimension, and it used
// the raw map lookup while every other command used the alias-tolerant one.
// The guard test existed but only exercised `query`.
func TestTopKeepsTimeBucketLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		io.WriteString(w, `{"results":[{"dimensions":{"time":"2026-09-02"},"metrics":{"visitors":210}}]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	out, _, code := run(t, env, "top", "time:day", "--metric", "visitors", "--json")
	if code != 0 {
		t.Fatalf("exit = %d (%q)", code, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["time"] != "2026-09-02" {
		t.Fatalf("bucket label lost: %v", rows[0])
	}
}

// TestSeriesCarriesCompare: series sent the compare and was billed for it,
// then dropped the answer in all three formats while stats, top and query all
// kept it.
func TestSeriesCarriesCompare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		io.WriteString(w, `{"results":[{"dimensions":{"time:day":"2026-09-02"},"metrics":{"visitors":210},
			"compare":{"visitors":{"value":100,"change":110}}}]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	out, _, _ := run(t, env, "series", "--compare", "previous", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["visitors_previous"] != float64(100) || rows[0]["visitors_change"] != float64(110) {
		t.Fatalf("compare missing from a series row: %v", rows[0])
	}

	human, _, _ := run(t, env, "series", "--compare", "previous")
	if !strings.Contains(human, "%") {
		t.Fatalf("human output should show the change, got %q", human)
	}
}

// TestPagingAdviceCountsRowsNotTheLimit: when the server returns fewer rows
// than the limit, advancing by the limit skips the rest.
func TestPagingAdviceCountsRowsNotTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		io.WriteString(w, `{"results":[
			{"dimensions":{"event:page":"/"},"metrics":{"visitors":1}},
			{"dimensions":{"event:page":"/b"},"metrics":{"visitors":2}}],
			"meta":{"total":57,"limit":25,"offset":0,"has_more":true}}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	_, errOut, _ := run(t, env, "top", "pages", "--limit", "25", "--format", "csv")
	// A substring check would pass on "--offset 25" too, which is exactly the
	// wrong answer this test exists to catch.
	m := regexp.MustCompile(`--offset (\d+)`).FindStringSubmatch(errOut)
	if m == nil {
		t.Fatalf("no paging advice: %q", errOut)
	}
	if m[1] != "2" {
		t.Fatalf("next offset = %s, want 2: only two rows were received", m[1])
	}
}

// TestSiteNotFoundIsRecoverable: a script that retries on 1 and pages a human
// on 2 should not wake anyone for a typo in a site name.
func TestSiteNotFoundIsRecoverable(t *testing.T) {
	f := newFakeAPI(t)
	out, _, code := run(t, f.env(t), "now", "--site", "typo.example", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1: naming a different site fixes this", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	steps, _ := v["next_steps"].([]any)
	if len(steps) == 0 {
		t.Fatalf("the choices must be listed: %v", v)
	}
}

// TestSitesHasNoTrailingWhitespace: the default marker is empty for every site
// but one, so keeping it last padded the column before it and left every other
// row ending in spaces.
func TestSitesHasNoTrailingWhitespace(t *testing.T) {
	f := newFakeAPI(t)
	out, _, _ := run(t, f.env(t), "sites")
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("line %d ends in whitespace: %q", i, line)
		}
	}
}

// TestVerboseShowsTheRequestID: the flag promised request ids and, on a
// successful call or in a machine format, did nothing whatsoever.
func TestVerboseShowsTheRequestID(t *testing.T) {
	f := newFakeAPI(t)
	_, quiet, _ := run(t, f.env(t), "sites", "--format", "csv")
	_, loud, _ := run(t, f.env(t), "sites", "--format", "csv", "--verbose")

	if strings.Contains(quiet, "request id") {
		t.Fatalf("the id should not be printed unasked on success: %q", quiet)
	}
	if !strings.Contains(loud, "request id") {
		t.Fatalf("--verbose must show the request id, got %q", loud)
	}
	if !strings.Contains(loud, "rate budget") {
		t.Fatalf("--verbose should also show what the call cost, got %q", loud)
	}
}

// TestEmptyBreakdownSaysSo: a blank screen with no explanation is the same
// "success with no answer" the totals path refuses.
func TestEmptyBreakdownSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":1,"name":"example.com","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	env["STATABLE_API_URL"] = srv.URL

	for _, args := range [][]string{{"top", "pages"}, {"series"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, errOut, _ := run(t, env, args...)
			if strings.TrimSpace(errOut) == "" {
				t.Fatalf("an empty result must be explained, got stdout %q and nothing on stderr", out)
			}
		})
	}
}

// TestUnusableFormatEnvIsReported: falling back to human without a word left
// the variable looking honoured while it was not.
func TestUnusableFormatEnvIsReported(t *testing.T) {
	env := emptyConfig(t)
	env["STATABLE_FORMAT"] = "xml"
	out, errOut, _ := run(t, env, "version")
	if !strings.Contains(errOut, "STATABLE_FORMAT") {
		t.Fatalf("an unusable value must be reported, got %q", errOut)
	}
	// Human output, not JSON. The version itself depends on how the binary
	// was built, so only its shape is pinned here.
	if !strings.HasPrefix(strings.TrimSpace(out), "statable ") {
		t.Fatalf("the fallback should still be human output, got %q", out)
	}

	good := emptyConfig(t)
	good["STATABLE_FORMAT"] = "json"
	jout, jerr, _ := run(t, good, "version")
	if strings.Contains(jerr, "STATABLE_FORMAT") {
		t.Fatalf("a usable value must not warn: %q", jerr)
	}
	if err := json.Unmarshal([]byte(jout), &map[string]any{}); err != nil {
		t.Fatalf("STATABLE_FORMAT=json must select JSON, got %q", jout)
	}
}

// TestLogoutWarningSurvivesQuiet: still being authenticated after logging out
// is exactly the class --quiet must not be able to hide.
func TestLogoutWarningSurvivesQuiet(t *testing.T) {
	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_key_long_enough_to_redact"
	_, errOut, _ := run(t, env, "auth", "logout", "--quiet", "--json")
	if !strings.Contains(errOut, "STATABLE_API_KEY") {
		t.Fatalf("--quiet must not hide that the shell is still authenticated, got %q", errOut)
	}
}
