package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The payloads below are copied verbatim from the API's own description, not
// written from memory of it.
//
// This file exists because of one defect that survived four review passes: the
// comparison block's percent field is called "change", the internal dashboard
// struct that looks like it spells the same thing calls it "diff", and reading
// the wrong layer made every comparison decode as zero. Silently, at exit 0,
// on a query the caller paid extra for. Nothing caught it because the fake
// server in the tests was written from the same misreading — a test and the
// code agreeing on a wrong field name prove each other, not the contract.
//
// So: when adding a field here, copy the shape from openapi.yaml or from a
// real response. Do not retype it from what the Go structs already say.

// specCompare is the shape openapi.yaml gives for a compare block:
// "Keyed by metric → {value, change}: the metric's value in the compare window
// and the percent change vs it."
const specCompare = `{"visitors":{"value":450,"change":11.1}}`

func TestCompareFieldNamesMatchTheSpec(t *testing.T) {
	var got map[string]CompareInfo
	if err := json.Unmarshal([]byte(specCompare), &got); err != nil {
		t.Fatal(err)
	}
	c, ok := got["visitors"]
	if !ok {
		t.Fatal("the block is keyed by metric")
	}
	if c.Value != 450 {
		t.Fatalf("value = %v, want 450", c.Value)
	}
	if c.Change == 0 {
		t.Fatalf("change decoded as zero: the JSON tag does not match the field the API sends")
	}
	if c.Change != 11.1 {
		t.Fatalf("change = %v, want 11.1", c.Change)
	}
}

// TestNoStructFieldIsNamedDiff guards the specific confusion: the API's
// internal row type spells this "diff" and the public one spells it "change".
// A field tagged diff anywhere in this package means someone read the wrong
// layer again.
func TestNoStructFieldIsNamedDiff(t *testing.T) {
	for _, v := range []any{CompareInfo{}, Result{}, Meta{}, Site{}, SiteStats{}, QueryRequest{}, QueryResponse{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			if name == "diff" {
				t.Fatalf("%s.%s is tagged \"diff\"; the public API calls it \"change\"",
					typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

// specSitesResponse is the example from endpoints.md, "List sites".
const specSitesResponse = `{
  "sites": [
    {
      "site_id": 3093477,
      "name": "example.com",
      "timezone": "Europe/Amsterdam",
      "hash": "03D3Cfb9eA",
      "stats_start_date": "2026-04-07",
      "created_at": "2026-02-21T12:52:54Z"
    }
  ]
}`

func TestSiteFieldNamesMatchTheSpec(t *testing.T) {
	var got SitesResponse
	if err := json.Unmarshal([]byte(specSitesResponse), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sites) != 1 {
		t.Fatalf("sites = %d", len(got.Sites))
	}
	s := got.Sites[0]
	if s.SiteID != 3093477 {
		t.Fatalf("site_id = %d", s.SiteID)
	}
	// Every documented field has to arrive, not only the ones a command
	// happens to print today.
	for name, ok := range map[string]bool{
		"name":             s.Name == "example.com",
		"timezone":         s.Timezone == "Europe/Amsterdam",
		"hash":             s.Hash == "03D3Cfb9eA",
		"created_at":       s.CreatedAt == "2026-02-21T12:52:54Z",
		"stats_start_date": s.StatsStartDate != nil && *s.StatsStartDate == "2026-04-07",
	} {
		if !ok {
			t.Fatalf("field %q did not decode", name)
		}
	}
}

// TestStatsStartDateMayBeNull: the docs say it is "First day with data, or
// null if it has not been computed", so the field has to be a pointer.
func TestStatsStartDateMayBeNull(t *testing.T) {
	var got SitesResponse
	err := json.Unmarshal([]byte(`{"sites":[{"site_id":1,"name":"x","timezone":"UTC","hash":"h","stats_start_date":null,"created_at":"x"}]}`), &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sites[0].StatsStartDate != nil {
		t.Fatal("a null start date must decode as absent, not as an empty string")
	}
}

// specBreakdownRow is the example from query.md, "Breakdown, with meta".
const specBreakdownRow = `{
  "results": [
    { "dimensions": { "visit:country": "US" }, "labels": { "visit:country": "United States" }, "metrics": { "visitors": 500 } }
  ],
  "meta": { "total": 57, "limit": 100, "offset": 0, "has_more": false }
}`

func TestBreakdownFieldNamesMatchTheSpec(t *testing.T) {
	var got QueryResponse
	if err := json.Unmarshal([]byte(specBreakdownRow), &got); err != nil {
		t.Fatal(err)
	}
	r := got.Results[0]
	if r.Dimensions["visit:country"] != "US" {
		t.Fatalf("dimensions did not decode: %v", r.Dimensions)
	}
	if r.Labels["visit:country"] != "United States" {
		t.Fatalf("labels did not decode: %v", r.Labels)
	}
	if v, ok := Num(r.Metrics["visitors"]); !ok || v != 500 {
		t.Fatalf("metrics did not decode: %v", r.Metrics)
	}
	if got.Meta == nil || got.Meta.Total != 57 || got.Meta.Limit != 100 || got.Meta.HasMore {
		t.Fatalf("meta did not decode: %+v", got.Meta)
	}
}

// specCurrentVisitors is the example from endpoints.md.
const specCurrentVisitors = `{ "site_id": 3093477, "visitors": 42 }`

func TestCurrentVisitorsMatchesTheSpec(t *testing.T) {
	var got CurrentVisitors
	if err := json.Unmarshal([]byte(specCurrentVisitors), &got); err != nil {
		t.Fatal(err)
	}
	if got.SiteID != 3093477 || got.Visitors != 42 {
		t.Fatalf("got %+v", got)
	}
}

// specError is the shape errors.md documents.
const specError = `{
  "code": "unknown_metric",
  "error": "unknown metric: foo",
  "request_id": "ch-node01-01997f2a8b3c7d5e8f01abcdef123456"
}`

func TestErrorFieldNamesMatchTheSpec(t *testing.T) {
	var got apiError
	if err := json.Unmarshal([]byte(specError), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "unknown_metric" || got.Error == "" || got.RequestID == "" {
		t.Fatalf("got %+v", got)
	}
}

// TestGoalReadsHostIDNotSiteID.
//
// GET /sites/{id}/goals returns the storage struct directly rather than a v1
// shape, so it is the one endpoint in the whole API where the site is called
// host_id. A Go field named SiteID tagged site_id compiles, reads naturally,
// and decodes to zero on every goal — the same silent failure that once made
// every comparison read as zero because the tag said diff instead of change.
//
// The payload below is copied from what apiListGoalsHandler marshals.
func TestGoalReadsHostIDNotSiteID(t *testing.T) {
	const body = `{"goals":[
		{"id":42,"host_id":7,"name":"Signup","event_name":"Signup",
		 "created_at":"2026-06-01T10:00:00Z","updated_at":"2026-06-01T10:00:00Z"}]}`

	var out GoalsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Goals) != 1 {
		t.Fatalf("got %d goals", len(out.Goals))
	}
	g := out.Goals[0]
	if g.SiteID != 7 {
		t.Fatalf("SiteID = %d, want 7: the server calls this field host_id here", g.SiteID)
	}
	if g.ID != 42 || g.Name != "Signup" {
		t.Fatalf("goal decoded wrong: %+v", g)
	}
}

// TestGoalKindCoversEveryShapeTheServerSends: the server fills exactly one of
// path, event_name and scroll_depth, and says nothing about which.
func TestGoalKindCoversEveryShapeTheServerSends(t *testing.T) {
	page, event := "/pricing", "Signup"
	depth := 90
	cases := []struct {
		goal   Goal
		kind   string
		target string
	}{
		{Goal{Path: &page}, "page", "/pricing"},
		{Goal{EventName: &event}, "event", "Signup"},
		{Goal{ScrollDepth: &depth}, "scroll", "90%"},
		{Goal{}, "", ""},
		// A zero scroll depth is a real depth, not an absent one.
		{Goal{ScrollDepth: new(int)}, "scroll", "0%"},
	}
	for _, tc := range cases {
		if got := tc.goal.Kind(); got != tc.kind {
			t.Errorf("Kind() = %q, want %q for %+v", got, tc.kind, tc.goal)
		}
		if got := tc.goal.Target(); got != tc.target {
			t.Errorf("Target() = %q, want %q for %+v", got, tc.target, tc.goal)
		}
	}
}

// TestSnippetFieldNames pins the three fields apiSiteSnippetHandler marshals.
func TestSnippetFieldNames(t *testing.T) {
	const body = `{"site_id":7,"script_url":"https://cdn.example/js/7/s.js",
		"snippet":"<script defer src=\"https://cdn.example/js/7/s.js\"></script>"}`

	var sn Snippet
	if err := json.Unmarshal([]byte(body), &sn); err != nil {
		t.Fatal(err)
	}
	if sn.SiteID != 7 {
		t.Errorf("SiteID = %d, want 7", sn.SiteID)
	}
	if sn.ScriptURL == "" || sn.Snippet == "" {
		t.Errorf("snippet decoded empty: %+v", sn)
	}
}

// TestCreatedKeyReadsTokenNotKey.
//
// POST /keys and POST /keys/{id}/rotate embed the APIKey struct and add the
// secret beside it as `token`. A field tagged `key` compiles, reads naturally,
// and decodes to "" -- and for this one response an empty value means the
// secret is lost, because the server never returns it again.
func TestCreatedKeyReadsTokenNotKey(t *testing.T) {
	const body = `{"id":42,"name":"ci","prefix":"stbl_abcd","scopes":"read",
		"website_id":null,"created_at":"2026-09-13T10:00:00Z","expires_at":null,
		"token":"stbl_the_actual_secret"}`

	var k CreatedKey
	if err := json.Unmarshal([]byte(body), &k); err != nil {
		t.Fatal(err)
	}
	if k.Token != "stbl_the_actual_secret" {
		t.Fatalf("Token = %q; the server calls the secret `token`, and it is shown once", k.Token)
	}
	// The embedded fields have to survive too.
	if k.ID != 42 || k.Name != "ci" || k.Prefix != "stbl_abcd" {
		t.Fatalf("the embedded key did not decode: %+v", k.APIKey)
	}
	if k.WebsiteID != nil {
		t.Errorf("website_id null means every site, not site zero")
	}
}

// TestKeyScopeMembership: Scopes is a comma-separated string, and a substring
// check would say a read-only key can manage keys.
func TestKeyScopeMembership(t *testing.T) {
	k := APIKey{Scopes: "read,sites:write"}
	for _, want := range []string{"read", "sites:write"} {
		if !k.Scope(want) {
			t.Errorf("Scope(%q) = false", want)
		}
	}
	for _, no := range []string{"keys:manage", "write", "read:all", ""} {
		if k.Scope(no) {
			t.Errorf("Scope(%q) = true, but it is not granted", no)
		}
	}
}

// TestSettingsTypesMatchTheServer.
//
// Every payload below is the shape the backend actually marshals, copied from
// the handler that builds it. The first version of these types was written
// from the OpenAPI summary and from a fake server I wrote from the same
// misreading, so the tests passed against a fiction while three commands could
// not decode a single real response.
func TestSettingsTypesMatchTheServer(t *testing.T) {
	t.Run("tracking", func(t *testing.T) {
		// api_v1_config.go: version is a number, enabled is a list of ids,
		// features are objects.
		const body = `{"site_id":7,"version":3,"bundle":"core",
			"enabled":["outbound","scroll"],
			"features":[{"id":"outbound","label":"Outbound links","enabled":true,
			             "locked":false,"default":false,"size_br":412}]}`
		var v TrackingSettings
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("a real tracking response does not decode: %v", err)
		}
		if v.Version != 3 || v.Bundle != "core" {
			t.Fatalf("decoded wrong: %+v", v)
		}
		if len(v.Enabled) != 2 || v.Enabled[0] != "outbound" {
			t.Fatalf("enabled is the list of ids that are on: %v", v.Enabled)
		}
		if len(v.Features) != 1 || v.Features[0].ID != "outbound" || !v.Features[0].Enabled {
			t.Fatalf("features are objects: %+v", v.Features)
		}
	})

	t.Run("countries read", func(t *testing.T) {
		// country_list.go: entries, not bare codes. Only a site with a country
		// actually listed exposes this, which is why an empty fixture hid it.
		const body = `{"allowed":[{"code":"UA","created_at":"2026-01-01T00:00:00Z"}],
			"blocked":[{"code":"RU","created_at":"2026-01-02T00:00:00Z"}]}`
		var v CountrySettings
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("a real country response does not decode: %v", err)
		}
		if got := Codes(v.Allowed); len(got) != 1 || got[0] != "UA" {
			t.Fatalf("allowed = %v", got)
		}
		if got := Codes(v.Blocked); len(got) != 1 || got[0] != "RU" {
			t.Fatalf("blocked = %v", got)
		}
	})

	t.Run("hostnames really are strings", func(t *testing.T) {
		// site_settings.go: this one is []string, which is what made the
		// country shape look safe by analogy.
		const body = `{"allowed":["example.com"],"blocked":["staging.example.com"]}`
		var v HostnameSettings
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		if len(v.Allowed) != 1 || v.Allowed[0] != "example.com" {
			t.Fatalf("decoded wrong: %+v", v)
		}
	})
}

// TestVerifyOTPNestsTheKey: api_v1_auth.go puts the key under "key" beside the
// token. Decoding it flat left id, prefix and scopes at zero while the token
// lined up, so the command looked like it worked.
func TestVerifyOTPNestsTheKey(t *testing.T) {
	const body = `{"token":"stbl_the_secret","created":true,
		"key":{"id":42,"name":"cli","prefix":"stbl_abcd","scopes":"read",
		       "website_id":null,"created_at":"2026-01-01T00:00:00Z"},
		"user":{"id":"u_1","email":"you@example.com"}}`

	var v VerifyOTPResponse
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if v.Token != "stbl_the_secret" {
		t.Fatalf("token = %q", v.Token)
	}
	if v.Key.ID != 42 || v.Key.Prefix != "stbl_abcd" || v.Key.Scopes != "read" {
		t.Fatalf("the key is nested under \"key\" and did not decode: %+v", v.Key)
	}
	if !v.Created {
		t.Error("created distinguishes a new account from a key on an existing one")
	}
}

// TestKeyEventHasNoID: api_keys.go sends event, ip, user_agent, created_at and
// actor_key_id. A column reading an absent id printed 0 on every row.
func TestKeyEventHasNoID(t *testing.T) {
	const body = `{"events":[{"event":"created","ip":"203.0.113.4",
		"user_agent":"statable-cli/0.2.2","created_at":"2026-01-01T00:00:00Z",
		"actor_key_id":null}]}`
	var out KeyEventsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	e := out.Events[0]
	if e.Event != "created" || e.IP == nil || *e.IP != "203.0.113.4" {
		t.Fatalf("decoded wrong: %+v", e)
	}
	if e.ActorKeyID != nil {
		t.Error("a null actor means the dashboard, not key zero")
	}
}
