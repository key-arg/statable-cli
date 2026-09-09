package api

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNumCoercesEveryShapeJSONProduces(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{float64(42), 42, true},
		{int(42), 42, true},
		{int64(42), 42, true},
		{json.Number("41.2"), 41.2, true},
		{json.Number("nonsense"), 0, false},
		{nil, 0, false},
		{"1204", 0, false},
		{true, 0, false},
	}
	for _, tc := range cases {
		got, ok := Num(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("Num(%v) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestNumHandlesANullRate: a rate is null when it is undefined for the window,
// and that must not become a silent zero.
func TestNumHandlesANullRate(t *testing.T) {
	var row Result
	if err := json.Unmarshal([]byte(`{"metrics":{"bounce_rate":null,"visitors":10}}`), &row); err != nil {
		t.Fatal(err)
	}
	if _, ok := Num(row.Metrics["bounce_rate"]); ok {
		t.Fatal("a null rate must not read as a number")
	}
	if v, ok := Num(row.Metrics["visitors"]); !ok || v != 10 {
		t.Fatalf("visitors = %v,%v", v, ok)
	}
}

// TestResolvedRangeReadsTheEcho: the server echoes the range it actually used
// as concrete dates, so the client never reproduces the calendar arithmetic.
func TestResolvedRangeReadsTheEcho(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"pair", `{"results":[],"query":{"date_range":["2026-09-02","2026-09-08"]}}`,
			[]string{"2026-09-02", "2026-09-08"}},
		{"single", `{"results":[],"query":{"date_range":"realtime"}}`, []string{"realtime"}},
		{"no echo", `{"results":[]}`, nil},
		{"echo without a range", `{"results":[],"query":{"metrics":["visitors"]}}`, nil},
		{"unexpected shape", `{"results":[],"query":{"date_range":{"from":"x"}}}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var q QueryResponse
			if err := json.Unmarshal([]byte(tc.body), &q); err != nil {
				t.Fatal(err)
			}
			got := q.ResolvedRange()
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestBreakdownRowDecodesLabelsAndCompare(t *testing.T) {
	var q QueryResponse
	body := `{"results":[{"dimensions":{"visit:country":"US"},"labels":{"visit:country":"United States"},
		"metrics":{"visitors":500},"compare":{"visitors":{"value":450,"change":11.1}}}],
		"meta":{"total":57,"limit":10,"offset":0,"has_more":true}}`
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatal(err)
	}
	r := q.Results[0]
	if r.Dimensions["visit:country"] != "US" || r.Labels["visit:country"] != "United States" {
		t.Fatalf("row = %+v", r)
	}
	c, ok := r.Compare["visitors"]
	if !ok || c.Value != 450 || math.Abs(c.Change-11.1) > 0.001 {
		t.Fatalf("compare = %+v", r.Compare)
	}
	if q.Meta == nil || !q.Meta.HasMore || q.Meta.Total != 57 {
		t.Fatalf("meta = %+v", q.Meta)
	}
}

// TestAggregateHasNoMeta: meta appears on breakdowns only, so a nil check is
// the difference between a paging hint and a panic.
func TestAggregateHasNoMeta(t *testing.T) {
	var q QueryResponse
	if err := json.Unmarshal([]byte(`{"results":[{"metrics":{"visitors":1}}]}`), &q); err != nil {
		t.Fatal(err)
	}
	if q.Meta != nil {
		t.Fatalf("meta = %+v, want nil on an aggregate", q.Meta)
	}
	if len(q.Results[0].Dimensions) != 0 {
		t.Fatal("an aggregate row carries no dimensions")
	}
}

// TestQueryRequestOmitsWhatMustBeAbsent: sending site_id, dimensions, limit or
// offset when they do not apply is rejected by the API.
func TestQueryRequestOmitsWhatMustBeAbsent(t *testing.T) {
	b, err := json.Marshal(QueryRequest{Metrics: []string{"visitors"}, DateRange: "7d"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"site_id", "dimensions", "filters", "compare", "limit", "offset"} {
		if _, present := m[k]; present {
			t.Fatalf("%q must be omitted when unset, got %s", k, b)
		}
	}
	for _, k := range []string{"metrics", "date_range"} {
		if _, present := m[k]; !present {
			t.Fatalf("%q is required and must always be sent, got %s", k, b)
		}
	}
}
