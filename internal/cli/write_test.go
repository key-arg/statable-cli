package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// writeServer records every request so a test can assert that nothing was
// sent, which is the only way to prove a confirmation actually guarded
// something.
type writeServer struct {
	mu   sync.Mutex
	seen []string
	srv  *httptest.Server
}

func newWriteServer(t *testing.T) (*writeServer, map[string]string) {
	t.Helper()
	w := &writeServer{}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.seen = append(w.seen, r.Method+" "+r.URL.Path+" "+string(body))
		w.mu.Unlock()

		rw.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/sites") && r.Method == http.MethodGet:
			io.WriteString(rw, `{"sites":[{"site_id":7,"name":"example.com","timezone":"UTC"}]}`)
		case strings.HasSuffix(p, "/sites") && r.Method == http.MethodPost:
			io.WriteString(rw, `{"site_id":9,"name":"new.example","timezone":"UTC","hash":"h",
				"script_url":"https://cdn/js/9/s.js","snippet":"<script></script>"}`)
		case strings.HasSuffix(p, "/keys") && r.Method == http.MethodPost:
			io.WriteString(rw, `{"id":1,"name":"ci","prefix":"stbl_ab","scopes":"read",
				"created_at":"2026-01-01T00:00:00Z","token":"stbl_the_secret"}`)
		case strings.HasSuffix(p, "/rotate"):
			io.WriteString(rw, `{"id":1,"name":"ci","prefix":"stbl_cd","scopes":"read",
				"created_at":"2026-01-01T00:00:00Z","token":"stbl_rotated"}`)
		case strings.HasSuffix(p, "/goals") && r.Method == http.MethodPost:
			io.WriteString(rw, `{"id":3,"host_id":7,"name":"Signup","event_name":"Signup",
				"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
		case strings.HasSuffix(p, "/funnels") && r.Method == http.MethodPost:
			io.WriteString(rw, `{"id":45,"site_id":7,"name":"F","scope":"visitor",
				"strict_order":false,"steps":[{"kind":"page"},{"kind":"event"}],
				"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
		case strings.Contains(p, "/settings/"):
			io.WriteString(rw, `{"site_id":7,"count":0,"enabled":true,"allowed":[],"blocked":[],
				"blocked_ips":[],"features":[],"version":"1","bundle":"a"}`)
		default:
			io.WriteString(rw, `{"ok":true}`)
		}
	}))
	t.Cleanup(w.srv.Close)

	return w, map[string]string{
		"STATABLE_CONFIG_DIR": t.TempDir(),
		"STATABLE_API_KEY":    "stbl_test_key_long_enough",
		"STATABLE_API_URL":    w.srv.URL + "/api/v1",
	}
}

func (w *writeServer) sent(method string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.seen {
		if strings.HasPrefix(s, method+" ") {
			return true
		}
	}
	return false
}

func (w *writeServer) bodyOf(method string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.seen {
		if strings.HasPrefix(s, method+" ") {
			parts := strings.SplitN(s, " ", 3)
			if len(parts) == 3 {
				return parts[2]
			}
		}
	}
	return ""
}

// TestDestructiveCommandsSendNothingWithoutConsent is the test that matters
// most in this file.
//
// Asserting on the error message would pass even if the request had already
// gone out. What has to be true is that the server was never asked, so the
// server records everything and the test checks that the destructive method
// is absent.
func TestDestructiveCommandsSendNothingWithoutConsent(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		method string
	}{
		{"sites delete", []string{"sites", "delete"}, "DELETE"},
		{"goals delete", []string{"goals", "delete", "3"}, "DELETE"},
		{"funnels delete", []string{"funnels", "delete", "45"}, "DELETE"},
		{"keys revoke", []string{"keys", "revoke", "1"}, "DELETE"},
		{"keys rotate", []string{"keys", "rotate", "1"}, "POST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, env := newWriteServer(t)
			_, _, code := run(t, env, tc.args...)
			if code != 1 {
				t.Errorf("exit = %d, want 1: it is fixable by adding --yes", code)
			}
			if ws.sent(tc.method) {
				t.Fatalf("%s was sent without consent", tc.method)
			}
		})
	}
}

// TestDestructiveCommandsProceedWithYes: the other half. A guard that can
// never be satisfied is as broken as one that never guards.
func TestDestructiveCommandsProceedWithYes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		method string
	}{
		{"sites delete", []string{"sites", "delete", "--yes"}, "DELETE"},
		{"goals delete", []string{"goals", "delete", "3", "--yes"}, "DELETE"},
		{"keys revoke", []string{"keys", "revoke", "1", "--yes"}, "DELETE"},
		{"keys rotate", []string{"keys", "rotate", "1", "--yes"}, "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, env := newWriteServer(t)
			out, errOut, code := run(t, env, tc.args...)
			if code != 0 {
				t.Fatalf("exit = %d: %s%s", code, out, errOut)
			}
			if !ws.sent(tc.method) {
				t.Fatalf("--yes was given and %s was still not sent", tc.method)
			}
		})
	}
}

// TestMintedSecretIsReadableAndNotLost: the secret is returned once, and the
// field is `token`. If this decodes wrong the user has a key they cannot use.
func TestMintedSecretIsReadableAndNotLost(t *testing.T) {
	for _, args := range [][]string{
		{"keys", "create", "ci", "--json"},
		{"keys", "rotate", "1", "--yes", "--json"},
	} {
		_, env := newWriteServer(t)
		out, _, code := run(t, env, args...)
		if code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, out)
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(out), &rec); err != nil {
			t.Fatalf("not JSON: %v (%q)", err, out)
		}
		tok, _ := rec["token"].(string)
		if !strings.HasPrefix(tok, "stbl_") {
			t.Fatalf("%v returned no usable secret: %v", args, rec)
		}
	}
}

// TestWriteCommandsRefuseAnEmptyReplacement.
//
// Every settings write is a PUT, so sending nothing would clear the setting.
// That is a legitimate thing to want and a terrible thing to do by accident,
// so it has to be spelled --clear.
func TestWriteCommandsRefuseAnEmptyReplacement(t *testing.T) {
	for _, args := range [][]string{
		{"settings", "set", "hostnames"},
		{"settings", "set", "countries"},
		{"settings", "set", "blocked-ips"},
		{"settings", "set", "tracking"},
	} {
		ws, env := newWriteServer(t)
		_, _, code := run(t, env, args...)
		if code != 64 {
			t.Errorf("%v exited %d, want 64", args, code)
		}
		if ws.sent("PUT") {
			t.Errorf("%v sent an empty replacement", args)
		}
	}

	// And --clear does go through, or the escape hatch is not one.
	ws, env := newWriteServer(t)
	if _, _, code := run(t, env, "settings", "set", "hostnames", "--clear"); code != 0 {
		t.Fatalf("--clear exited %d", code)
	}
	if !ws.sent("PUT") {
		t.Fatal("--clear did not send the replacement")
	}
}

// TestGoalMatchesExactlyOneThing: the server fills one of path, event_name
// and scroll_depth. Two would be a goal that cannot say what it matches.
func TestGoalMatchesExactlyOneThing(t *testing.T) {
	ws, env := newWriteServer(t)
	if _, _, code := run(t, env, "goals", "create", "--name", "X"); code != 64 {
		t.Errorf("a goal with no matcher exited %d, want 64", code)
	}
	if _, _, code := run(t, env, "goals", "create", "--name", "X",
		"--path", "/a", "--event", "B"); code != 64 {
		t.Errorf("a goal with two matchers exited %d, want 64", code)
	}
	if ws.sent("POST") {
		t.Fatal("an impossible goal was sent to the server")
	}

	ws2, env2 := newWriteServer(t)
	if _, _, code := run(t, env2, "goals", "create", "--name", "X", "--event", "Signup"); code != 0 {
		t.Fatalf("a valid goal exited %d", code)
	}
	if b := ws2.bodyOf("POST"); !strings.Contains(b, `"event_name":"Signup"`) {
		t.Fatalf("the body did not carry the event: %s", b)
	}
}

// TestFunnelStepsAreParsedIntoTheWireShape: the shorthand is the whole point
// of the command, and it has to produce what the API expects.
func TestFunnelStepsAreParsedIntoTheWireShape(t *testing.T) {
	ws, env := newWriteServer(t)
	_, _, code := run(t, env, "funnels", "create", "--name", "F",
		"--step", "page:/pricing", "--step", "event:Signup", "--step", "goal:9",
		"--step", "scroll:90", "--step", "entry:/landing")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	body := ws.bodyOf("POST")
	for _, want := range []string{
		`"kind":"page"`, `"path":"/pricing"`, `"operator":"e"`,
		`"kind":"event"`, `"event":"Signup"`,
		`"kind":"goal"`, `"goal_id":9`,
		`"kind":"scroll"`, `"threshold":90`,
		`"kind":"entry_page"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body is missing %s:\n%s", want, body)
		}
	}

	// A funnel is about what happens between steps, so one step is not one.
	ws2, env2 := newWriteServer(t)
	if _, _, code := run(t, env2, "funnels", "create", "--name", "F",
		"--step", "page:/only"); code != 64 {
		t.Errorf("a one-step funnel exited %d, want 64", code)
	}
	if ws2.sent("POST") {
		t.Error("a one-step funnel was sent")
	}
}

// TestSiteEditSendsOnlyWhatChanged: PATCH means patch. Sending a field the
// user did not mention would overwrite a setting they never touched.
func TestSiteEditSendsOnlyWhatChanged(t *testing.T) {
	ws, env := newWriteServer(t)
	if _, _, code := run(t, env, "sites", "edit", "--timezone", "Europe/Kyiv"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	body := ws.bodyOf("PATCH")
	if !strings.Contains(body, `"timezone":"Europe/Kyiv"`) {
		t.Fatalf("the change was not sent: %s", body)
	}
	for _, absent := range []string{`"url"`, `"week_start"`} {
		if strings.Contains(body, absent) {
			t.Errorf("%s was sent although it was never given: %s", absent, body)
		}
	}
}

// TestWeekStartAcceptsNamesAndNumbers: the API's 0-to-6 is unmemorable, and
// getting it wrong silently shifts every weekly figure.
func TestWeekStartAcceptsNamesAndNumbers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"sunday", `"week_start":0`},
		{"monday", `"week_start":1`},
		{"mon", `"week_start":1`},
		{"6", `"week_start":6`},
	} {
		ws, env := newWriteServer(t)
		if _, _, code := run(t, env, "sites", "edit", "--week-start", tc.in); code != 0 {
			t.Fatalf("--week-start %s exited %d", tc.in, code)
		}
		if b := ws.bodyOf("PATCH"); !strings.Contains(b, tc.want) {
			t.Errorf("--week-start %s sent %s, want %s", tc.in, b, tc.want)
		}
	}
	ws, env := newWriteServer(t)
	if _, _, code := run(t, env, "sites", "edit", "--week-start", "someday"); code != 64 {
		t.Error("a day that does not exist was accepted")
	}
	if ws.sent("PATCH") {
		t.Error("an invalid week start was sent")
	}
}
