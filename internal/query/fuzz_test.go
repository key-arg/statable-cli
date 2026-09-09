package query

import (
	"strings"
	"testing"
)

// FuzzParseFilter drives the filter parser with arbitrary input. It must
// never panic, and whatever it accepts must round-trip into a filter the API
// would recognise: a parser that accepts something malformed is worse than
// one that rejects something valid, because the failure then happens on the
// server with a message about a field the user never typed.
func FuzzParseFilter(f *testing.F) {
	for _, seed := range []string{
		"country=US", "country!=US", "page~/blog", "page!~/admin",
		"country=US,DE", "event=Signup", "", "=", "~", "!=", "!~",
		"country", "=US", "country=", "visit:country=US",
		"page=/~alice", "referrer=https://x.com/a=b", "country = US ",
		"event=pageview", "event=A,B", "\x00=\x00", strings.Repeat("a", 5000) + "=b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := ParseFilter(in)
		if err != nil {
			return
		}
		if got.Field == "" {
			t.Fatalf("accepted %q with no field", in)
		}
		if !FilterFields[got.Field] {
			t.Fatalf("accepted %q with unknown field %q", in, got.Field)
		}
		switch got.Operator {
		case "is", "is_not", "contains", "does_not_contain":
		default:
			t.Fatalf("accepted %q with unknown operator %q", in, got.Operator)
		}
		if len(got.Values) == 0 {
			t.Fatalf("accepted %q with no values", in)
		}
		for _, v := range got.Values {
			if v == "" {
				t.Fatalf("accepted %q with an empty value", in)
			}
			// The API forbids a comma inside a value, which is what lets the
			// CLI use it as the OR separator. Emitting one would produce a
			// filter the server cannot represent.
			if strings.Contains(v, ",") {
				t.Fatalf("value %q from %q contains a comma", v, in)
			}
		}
		if got.Field == "event" {
			if got.Operator != "is" || len(got.Values) != 1 {
				t.Fatalf("the event filter must stay single-valued and exact: %+v", got)
			}
		}
	})
}

// FuzzParseDateRange must never panic, and every accepted value must be one of
// the two shapes the API takes.
func FuzzParseDateRange(f *testing.F) {
	for _, seed := range []string{
		"7d", "30d", "1d", "90d", "month", "realtime",
		"2026-06-01..2026-06-30", "2026-06-01,2026-06-30",
		"", "0d", "91d", "007d", "yesterday", "..", ",",
		"2026-99-99..2026-99-99", "2026-06-30..2026-06-01",
		"9999999999999999999d", "-1d", "2026-06-01..",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := ParseDateRange(in)
		if err != nil {
			return
		}
		switch v := got.(type) {
		case string:
			if v != "month" && v != "realtime" && !daysRange.MatchString(v) {
				t.Fatalf("accepted %q as the preset %q", in, v)
			}
		case []string:
			if len(v) != 2 {
				t.Fatalf("accepted %q as a %d-element pair", in, len(v))
			}
			for _, d := range v {
				if !dateLiteral.MatchString(d) {
					t.Fatalf("accepted %q with the non-date %q", in, d)
				}
			}
			if v[0] > v[1] {
				t.Fatalf("accepted %q with the range inverted", in)
			}
		default:
			t.Fatalf("accepted %q as an unexpected type %T", in, got)
		}
	})
}
