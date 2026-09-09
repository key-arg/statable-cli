package query

import (
	"strings"
	"testing"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
)

func TestParseDateRange(t *testing.T) {
	cases := []struct {
		in      string
		want    any
		wantErr bool
	}{
		{"7d", "7d", false},
		{"30d", "30d", false},
		{"1d", "1d", false},
		{"90d", "90d", false},
		{"month", "month", false},
		{"realtime", "realtime", false},
		{"2026-06-01..2026-06-30", []string{"2026-06-01", "2026-06-30"}, false},
		{"2026-06-01,2026-06-30", []string{"2026-06-01", "2026-06-30"}, false},
		{"2026-06-01..2026-06-01", []string{"2026-06-01", "2026-06-01"}, false},
		{"", nil, true},
		{"0d", nil, true},
		{"91d", nil, true},
		{"yesterday", nil, true},
		// "today" and "day" are not in query.md's table. Accepting them here
		// meant the local validator approved a value the API answers with 400.
		{"today", nil, true},
		{"day", nil, true},
		{"007d", nil, true},
		{"2026-99-99..2026-99-99", nil, true},
		{"2026-02-30..2026-03-01", nil, true},
		{"2015-01-01..2026-01-01", nil, true},
		{"2026-06-30..2026-06-01", nil, true},
		{"2026-6-1..2026-6-30", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDateRange(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch want := tc.want.(type) {
			case string:
				if got != want {
					t.Fatalf("got %v, want %v", got, want)
				}
			case []string:
				gs, ok := got.([]string)
				if !ok || len(gs) != 2 || gs[0] != want[0] || gs[1] != want[1] {
					t.Fatalf("got %v, want %v", got, want)
				}
			default:
				// Without this a future case whose expectation is neither
				// shape would assert nothing and pass.
				t.Fatalf("unhandled expectation type %T; the case asserts nothing", tc.want)
			}
		})
	}
}

// TestRelativeRangeCapMatchesTheAPI: the API accepts 1..90 days and wants an
// explicit pair beyond that, so a longer window must be caught here with an
// explanation rather than relayed as a 400.
func TestRelativeRangeCapMatchesTheAPI(t *testing.T) {
	_, err := ParseDateRange("120d")
	if err == nil {
		t.Fatal("120d must be refused")
	}
	if !strings.Contains(err.Error(), "two dates") {
		t.Fatalf("the message should name the way out, got %q", err)
	}
}

func TestParseFilterOperators(t *testing.T) {
	cases := []struct {
		in   string
		want api.Filter
	}{
		{"country=US", api.Filter{Field: "country", Operator: "is", Values: []string{"US"}}},
		{"country!=US", api.Filter{Field: "country", Operator: "is_not", Values: []string{"US"}}},
		{"page~/blog", api.Filter{Field: "page", Operator: "contains", Values: []string{"/blog"}}},
		{"page!~/admin", api.Filter{Field: "page", Operator: "does_not_contain", Values: []string{"/admin"}}},
		{"country=US,DE,FR", api.Filter{Field: "country", Operator: "is", Values: []string{"US", "DE", "FR"}}},
		{" country = US ", api.Filter{Field: "country", Operator: "is", Values: []string{"US"}}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseFilter(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Field != tc.want.Field || got.Operator != tc.want.Operator {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if len(got.Values) != len(tc.want.Values) {
				t.Fatalf("values = %v, want %v", got.Values, tc.want.Values)
			}
			for i := range got.Values {
				if got.Values[i] != tc.want.Values[i] {
					t.Fatalf("values = %v, want %v", got.Values, tc.want.Values)
				}
			}
		})
	}
}

// TestNotEqualsBeatsEquals: "!=" contains "=", so the order the operators are
// tested in decides whether country!=US parses as is_not or as is with a
// value of "=US".
func TestNotEqualsBeatsEquals(t *testing.T) {
	f, err := ParseFilter("country!=US")
	if err != nil {
		t.Fatal(err)
	}
	if f.Operator != "is_not" || f.Values[0] != "US" {
		t.Fatalf("got %+v", f)
	}
	g, err := ParseFilter("page!~/x")
	if err != nil {
		t.Fatal(err)
	}
	if g.Operator != "does_not_contain" || g.Values[0] != "/x" {
		t.Fatalf("got %+v", g)
	}
}

// TestDimensionPrefixInAFilterIsExplained: the dimension is visit:country but
// the filter field is plain country, which is a documented trap.
func TestDimensionPrefixInAFilterIsExplained(t *testing.T) {
	_, err := ParseFilter("visit:country=US")
	if err == nil {
		t.Fatal("a prefixed field must be refused")
	}
	if !strings.Contains(err.Error(), "write country") {
		t.Fatalf("the message should name the correction, got %q", err)
	}
}

func TestEventFilterRestrictions(t *testing.T) {
	if _, err := ParseFilter("event=Signup"); err != nil {
		t.Fatalf("a single event is valid: %v", err)
	}
	for _, bad := range []string{"event!=Signup", "event~Sign", "event=A,B", "event=pageview", "event=engagement"} {
		if _, err := ParseFilter(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestFilterErrors(t *testing.T) {
	for _, bad := range []string{"country", "=US", "nosuchfield=1", "country="} {
		if _, err := ParseFilter(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func base() Spec {
	return Spec{SiteID: 1, Metrics: []string{"visitors"}, DateRange: "7d"}
}

func TestBuildRejectsWhatTheAPIWould(t *testing.T) {
	cases := []struct {
		name string
		spec func(Spec) Spec
		code string
	}{
		{"no metrics", func(s Spec) Spec { s.Metrics = nil; return s }, "METRICS_REQUIRED"},
		// api_v1.go rejects this every time with compare_length_mismatch: a
		// preset's main range ends at the moment of the query, never at a
		// midnight, so its span is not a whole number of days and an explicit
		// pair cannot equal it. Sending it spends a call to be told so.
		{"explicit compare against a preset range", func(s Spec) Spec {
			s.Compare = "2026-05-01..2026-05-07"
			return s
		}, "INVALID_COMPARE"},
		{"explicit compare against realtime", func(s Spec) Spec {
			s.DateRange = "realtime"
			s.Compare = "2026-05-01..2026-05-07"
			return s
		}, "INVALID_COMPARE"},
		{"unknown metric", func(s Spec) Spec { s.Metrics = []string{"clicks"}; return s }, "UNKNOWN_METRIC"},
		{"breakdown metric on totals", func(s Spec) Spec {
			s.Metrics = []string{"time_on_page"}
			return s
		}, "METRIC_NOT_AVAILABLE"},
		{"metric the dimension does not compute", func(s Spec) Spec {
			s.Dimension = "visit:country"
			s.Metrics = []string{"pageviews"}
			return s
		}, "METRIC_NOT_AVAILABLE"},
		{"unknown dimension", func(s Spec) Spec { s.Dimension = "visit:phase_of_moon"; return s }, "UNKNOWN_DIMENSION"},
		{"props without an event filter", func(s Spec) Spec {
			s.Dimension = "event:props:plan"
			return s
		}, "EVENT_FILTER_REQUIRED"},
		{"limit on totals", func(s Spec) Spec { s.Limit, s.HasLimit = 10, true; return s }, "LIMIT_OFFSET_MISUSE"},
		{"limit on a series", func(s Spec) Spec {
			s.Dimension = "time:day"
			s.Limit, s.HasLimit = 10, true
			return s
		}, "LIMIT_OFFSET_MISUSE"},
		{"limit out of range", func(s Spec) Spec {
			s.Dimension = "event:page"
			s.Limit, s.HasLimit = 5000, true
			return s
		}, "LIMIT_OFFSET_MISUSE"},
		{"minute outside realtime", func(s Spec) Spec { s.Dimension = "time:minute"; return s }, "INVALID_INTERVAL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.spec(base()).Build()
			if err == nil {
				t.Fatal("want an error")
			}
			if got := clierr.From(err).Code(); got != tc.code {
				t.Fatalf("code = %q, want %q (%v)", got, tc.code, err)
			}
		})
	}
}

func TestBuildAcceptsValidShapes(t *testing.T) {
	cases := []struct {
		name string
		spec func(Spec) Spec
	}{
		{"totals", func(s Spec) Spec { return s }},
		{"series", func(s Spec) Spec { s.Dimension = "time:day"; return s }},
		{"breakdown with paging", func(s Spec) Spec {
			s.Dimension = "event:page"
			s.Metrics = []string{"visitors", "pageviews"}
			s.Limit, s.HasLimit = 25, true
			s.Offset, s.HasOffset = 50, true
			return s
		}},
		{"props with an event filter", func(s Spec) Spec {
			s.Dimension = "event:props:plan"
			s.Metrics = []string{"visitors", "events"}
			s.Filters = []api.Filter{{Field: "event", Operator: "is", Values: []string{"Signup"}}}
			return s
		}},
		{"minute inside realtime", func(s Spec) Spec {
			s.Dimension = "time:minute"
			s.DateRange = "realtime"
			return s
		}},
		{"compare previous", func(s Spec) Spec { s.Compare = "previous_period"; return s }},
		// An explicit compare pair goes with an explicit range pair. Against a
		// preset it is refused, and the rejection table below covers that:
		// the server's main range for 7d ends at the current instant, so no
		// fixed pair of dates can ever span the same length.
		{"compare a named pair against an explicit range", func(s Spec) Spec {
			s.DateRange = "2026-04-01..2026-04-30"
			s.Compare = "2026-05-01..2026-05-30"
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.spec(base()).Build(); err != nil {
				t.Fatalf("should be accepted: %v", err)
			}
		})
	}
}

// TestLimitOmittedWhenNotAsked: sending limit on an aggregate is rejected by
// the API, so the field must be absent rather than zero.
func TestLimitOmittedWhenNotAsked(t *testing.T) {
	req, err := base().Build()
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit != nil || req.Offset != nil {
		t.Fatalf("limit/offset must be absent, got %v/%v", req.Limit, req.Offset)
	}
	if len(req.Dimensions) != 0 {
		t.Fatalf("dimensions must be absent on an aggregate, got %v", req.Dimensions)
	}
}

// TestOffsetZeroIsStillSent: zero is a meaningful offset, so "not asked" and
// "asked for zero" cannot be the same thing.
func TestOffsetZeroIsStillSent(t *testing.T) {
	s := base()
	s.Dimension = "event:page"
	s.Offset, s.HasOffset = 0, true
	req, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	if req.Offset == nil || *req.Offset != 0 {
		t.Fatalf("offset = %v, want an explicit zero", req.Offset)
	}
}

func TestCompareRejectsNonsense(t *testing.T) {
	s := base()
	s.Compare = "last week"
	if _, err := s.Build(); err == nil {
		t.Fatal("an unparseable compare must be refused")
	}
	s.Compare = "7d"
	if _, err := s.Build(); err == nil {
		t.Fatal("a relative compare window must be refused; the API wants a pair")
	}
}

func TestDescribeRange(t *testing.T) {
	if got := DescribeRange([]string{"2026-09-01", "2026-09-07"}); got != "2026-09-01 to 2026-09-07" {
		t.Fatalf("got %q", got)
	}
	if got := DescribeRange([]string{"2026-09-01", "2026-09-01"}); got != "2026-09-01" {
		t.Fatalf("a single day should not read as a range, got %q", got)
	}
	if got := DescribeRange(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

// TestExplicitRangeCapMatchesTheAPI pins the boundary to the server's own
// rule rather than to a plausible reading of the prose.
//
// api_v1.go rejects when to.Sub(from) is more than 366 days: a *span* between
// the two dates, not a count of the days they cover. Counting inclusive days
// and refusing more than 366 is one day stricter, and it refused a range the
// server answers. The old assertions below encoded that mistake, which is why
// they passed while the CLI was wrong.
func TestExplicitRangeCapMatchesTheAPI(t *testing.T) {
	ok := []string{
		"2026-01-01..2026-12-31", // 364-day span
		"2024-01-01..2024-12-31", // 365-day span, leap year
		"2024-01-01..2025-01-01", // exactly 366 days apart: the last legal one
	}
	for _, r := range ok {
		if _, err := ParseDateRange(r); err != nil {
			t.Fatalf("%s must be accepted, the API accepts it: %v", r, err)
		}
	}
	if _, err := ParseDateRange("2024-01-01..2025-01-02"); err == nil {
		t.Fatal("a 367-day span must be refused")
	}
}

// TestFilterValueMayContainAnOperatorCharacter: a /~user path is common, and
// scanning by operator instead of by position read it as the contains
// operator and told the user their field was called "page=/".
func TestFilterValueMayContainAnOperatorCharacter(t *testing.T) {
	f, err := ParseFilter("page=/~alice")
	if err != nil {
		t.Fatal(err)
	}
	if f.Operator != "is" || f.Values[0] != "/~alice" {
		t.Fatalf("got %+v", f)
	}
	g, err := ParseFilter("referrer=https://x.com/a=b")
	if err != nil {
		t.Fatal(err)
	}
	if g.Operator != "is" || g.Values[0] != "https://x.com/a=b" {
		t.Fatalf("got %+v", g)
	}
	h, err := ParseFilter("page~/~alice")
	if err != nil {
		t.Fatal(err)
	}
	if h.Operator != "contains" || h.Values[0] != "/~alice" {
		t.Fatalf("got %+v", h)
	}
	// At the same position the longer token wins.
	n, err := ParseFilter("country!=US")
	if err != nil {
		t.Fatal(err)
	}
	if n.Operator != "is_not" {
		t.Fatalf("got %+v", n)
	}
}

// TestPropsDimensionValidatesItsMetrics: event:props:<key> computes visitors
// and events only, and used to skip metric validation entirely.
func TestPropsDimensionValidatesItsMetrics(t *testing.T) {
	s := base()
	s.Dimension = "event:props:plan"
	s.Filters = []api.Filter{{Field: "event", Operator: "is", Values: []string{"Signup"}}}

	s.Metrics = []string{"visitors", "events"}
	if _, err := s.Build(); err != nil {
		t.Fatalf("its own metrics must be accepted: %v", err)
	}

	s.Metrics = []string{"bounce_rate"}
	if _, err := s.Build(); err == nil {
		t.Fatal("a metric this dimension does not compute must be refused")
	}
}

// TestUnknownMetricNamesTheRightAlternatives: offering engagement_time for a
// page breakdown, where it does not exist, while hiding time_on_page, where
// it does, is worse than saying nothing.
func TestUnknownMetricNamesTheRightAlternatives(t *testing.T) {
	s := base()
	s.Dimension = "event:page"
	s.Metrics = []string{"clicks"}
	_, err := s.Build()
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "time_on_page") {
		t.Fatalf("the list should name what this dimension computes, got %q", msg)
	}
	if strings.Contains(msg, "engagement_time") {
		t.Fatalf("the list should not offer a metric this dimension lacks, got %q", msg)
	}
}

func TestNegativeOffsetIsRefused(t *testing.T) {
	s := base()
	s.Dimension = "event:page"
	s.Offset, s.HasOffset = -5, true
	if _, err := s.Build(); err == nil {
		t.Fatal("a negative offset must be refused rather than forwarded")
	}
}

// TestPropsDimensionIsSuggested: telling a user that a supported dimension
// does not exist is worse than an incomplete list.
func TestPropsDimensionIsSuggested(t *testing.T) {
	s := base()
	s.Dimension = "visit:phase_of_moon"
	_, err := s.Build()
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "event:props:") {
		t.Fatalf("the suggestion list should include the property dimension, got %q", err)
	}
}

// TestPropertyKeyMayContainASpace: the server accepts any non-empty key after
// event:props:, and a custom property is a name a person chose, so "sign up"
// is as legal as "plan". Forbidding whitespace refused a breakdown the API
// answers.
func TestPropertyKeyMayContainASpace(t *testing.T) {
	for _, dim := range []string{
		"event:props:plan",
		"event:props:sign up",
		"event:props:Button Clicked",
	} {
		spec := Spec{
			Metrics:   []string{"visitors"},
			DateRange: "7d",
			Dimension: dim,
			Filters:   []api.Filter{{Field: "event", Operator: "is", Values: []string{"Signup"}}},
			SiteID:    1,
		}
		if _, err := spec.Build(); err != nil {
			t.Errorf("%q must be accepted, the API accepts it: %v", dim, err)
		}
	}
	// The key still has to exist.
	spec := Spec{
		Metrics: []string{"visitors"}, DateRange: "7d",
		Dimension: "event:props:", SiteID: 1,
		Filters: []api.Filter{{Field: "event", Operator: "is", Values: []string{"Signup"}}},
	}
	if _, err := spec.Build(); err == nil {
		t.Error("an empty property key must be refused")
	}
}
