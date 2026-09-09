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
