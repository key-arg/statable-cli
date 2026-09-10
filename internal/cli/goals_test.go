package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// goalsServer answers with the shape the backend actually returns: the storage
// struct, which calls the site host_id rather than site_id.
func goalsServer(t *testing.T, writeDisabled bool) (*httptest.Server, map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sites") {
			io.WriteString(w, `{"sites":[{"site_id":7,"name":"example.com","timezone":"UTC"}]}`)
			return
		}
		if writeDisabled {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"write_disabled","error":"not found"}`)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/goals"):
			io.WriteString(w, `{"goals":[
				{"id":1,"host_id":7,"name":"Signup","event_name":"Signup",
				 "created_at":"2026-06-01T10:00:00Z","updated_at":"2026-06-01T10:00:00Z"},
				{"id":2,"host_id":7,"name":"Pricing","path":"/pricing","operator":"e",
				 "created_at":"2026-06-02T10:00:00Z","updated_at":"2026-06-02T10:00:00Z"},
				{"id":3,"host_id":7,"name":"Read","scroll_depth":90,
				 "created_at":"2026-06-03T10:00:00Z","updated_at":"2026-06-03T10:00:00Z"}]}`)
		case strings.HasSuffix(r.URL.Path, "/snippet"):
			io.WriteString(w, `{"site_id":7,"script_url":"https://cdn.example/js/7/s.js",
				"snippet":"<script defer src=\"https://cdn.example/js/7/s.js\"></script>"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"not_found","error":"nope"}`)
		}
	}))
	env := map[string]string{
		"STATABLE_CONFIG_DIR": t.TempDir(),
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		"STATABLE_API_URL":    srv.URL + "/api/v1",
	}
	return srv, env
}

// TestGoalKindIsNotLeftToBeInferred.
//
// The server says what a goal matches by which field it fills, and leaves the
// other two absent. A table that printed the three columns raw would show a
// name and two blanks, and the reader would have to know the rule. The kind is
// derived once, where the rule is written down.
func TestGoalKindIsNotLeftToBeInferred(t *testing.T) {
	srv, env := goalsServer(t, false)
	defer srv.Close()

	out, _, code := run(t, env, "goals", "--json")
	if code != 0 {
		t.Fatalf("exit = %d (%q)", code, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out)
	}
	want := []struct{ kind, target string }{
		{"event", "Signup"},
		{"page", "/pricing"},
		{"scroll", "90%"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d goals, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i]["kind"] != w.kind || rows[i]["target"] != w.target {
			t.Errorf("goal %d = %v/%v, want %s/%s",
				i, rows[i]["kind"], rows[i]["target"], w.kind, w.target)
		}
	}
}

// TestSnippetHumanOutputIsTheTagAlone: the point of the command is that
// `statable snippet | pbcopy` puts something installable on the clipboard.
// A label, a heading or a second field would make that paste broken HTML.
func TestSnippetHumanOutputIsTheTagAlone(t *testing.T) {
	srv, env := goalsServer(t, false)
	defer srv.Close()

	out, _, code := run(t, env, "snippet")
	if code != 0 {
		t.Fatalf("exit = %d (%q)", code, out)
	}
	got := strings.TrimSpace(out)
	if !strings.HasPrefix(got, "<script") || !strings.HasSuffix(got, "</script>") {
		t.Fatalf("human output is not a bare tag:\n%s", out)
	}
	if strings.Contains(got, "script_url") || strings.Contains(got, "site_id") {
		t.Fatalf("human output carries fields that break a paste:\n%s", out)
	}

	// The machine formats still carry everything.
	out, _, _ = run(t, env, "snippet", "--json")
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out)
	}
	for _, k := range []string{"snippet", "script_url", "site_id"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("%s is missing from %v", k, rec)
		}
	}
}

// TestWriteDisabledIsExplained.
//
// Both endpoints sit behind the API's write guard even though reading them
// needs only the read scope. A deployment with writes switched off answers
// 404 write_disabled, which reads as "no such site" to anyone who does not
// know that. It is not: the site is fine, the key is fine, and retrying will
// never help, so it has to say what is actually wrong.
func TestWriteDisabledIsExplained(t *testing.T) {
	srv, env := goalsServer(t, true)
	defer srv.Close()

	for _, cmd := range []string{"goals", "snippet"} {
		out, errOut, code := run(t, env, cmd)
		if code != 1 {
			t.Errorf("%s exited %d, want 1: it is fixable, not a failure", cmd, code)
		}
		text := out + errOut
		if !strings.Contains(text, "write surface") {
			t.Errorf("%s does not explain write_disabled:\n%s", cmd, text)
		}
		if strings.Contains(text, "no such site") || strings.Contains(text, "unknown site") {
			t.Errorf("%s blamed the site:\n%s", cmd, text)
		}
	}
}

// TestUnknownSnippetTypeIsRefusedBeforeSending: the server answers an unknown
// type with a 400 that does not list the ones that exist.
func TestUnknownSnippetTypeIsRefusedBeforeSending(t *testing.T) {
	env := emptyConfig(t)
	env["STATABLE_API_KEY"] = "stbl_test_key_long_enough"
	// Nothing listens here: if the check ever stops happening locally, the
	// command fails with a network error and a different code.
	env["STATABLE_API_URL"] = "http://127.0.0.1:1/api/v1"

	_, errOut, code := run(t, env, "snippet", "--type", "bogus")
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	for _, want := range snippetTypes {
		if !strings.Contains(errOut, want) {
			t.Errorf("the error does not name %q as a choice:\n%s", want, errOut)
		}
	}
}
