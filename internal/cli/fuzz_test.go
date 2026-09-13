package cli

import (
	"strings"
	"testing"
)

// FuzzParseFunnelStep: the step shorthand is a parser that takes whatever a
// user types, and every other parser in this program got fuzzed before it
// shipped. A step must either parse into a shape the API accepts or be
// refused; it must never panic, and it must never return a step with no kind,
// which the server would reject with a message about a field nobody typed.
func FuzzParseFunnelStep(f *testing.F) {
	for _, seed := range []string{
		"page:/pricing", "page:/blog:begins", "page:/x:contains",
		"event:Signup", "goal:9", "scroll:90",
		"entry:/landing", "exit:/checkout",
		"", ":", "page:", ":/pricing", "page", "goal:abc", "scroll:0",
		"scroll:101", "scroll:-1", "PAGE:/UPPER", "page:/a:bogus",
		"goal:99999999999999999999", "page:/\x00", "event:\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		step, err := parseFunnelStep(in)
		if err != nil {
			return
		}
		if step.Kind == "" {
			t.Fatalf("parseFunnelStep(%q) accepted a step with no kind", in)
		}
		// Whatever it accepted, exactly one of the value fields carries the
		// step's meaning. A step with none is one the server cannot run.
		set := 0
		if step.Path != nil {
			set++
		}
		if step.Event != nil {
			set++
		}
		if step.GoalID != nil {
			set++
		}
		if step.Threshold != nil {
			set++
		}
		if set != 1 {
			t.Fatalf("parseFunnelStep(%q) set %d value fields, want exactly 1: %+v",
				in, set, step)
		}
		// A path step always carries an operator the API knows.
		if step.Path != nil {
			if step.Operator == nil {
				t.Fatalf("parseFunnelStep(%q) made a path step with no operator", in)
			}
			// funnels.go documents kind=page as (e|b|c|r), the same four goals
			// take. The fuzzer found this the moment r was added to the parser
			// and not to the invariant.
			switch *step.Operator {
			case "e", "b", "c", "r":
			default:
				t.Fatalf("parseFunnelStep(%q) produced operator %q, which the API does not know",
					in, *step.Operator)
			}
		}
		// 0 is a real depth on the server: the page was reached without
		// scrolling. The floor here was 1 and the fuzzer caught the day the
		// parser was corrected and this was not.
		if step.Threshold != nil && (*step.Threshold < 0 || *step.Threshold > 100) {
			t.Fatalf("parseFunnelStep(%q) accepted scroll depth %d", in, *step.Threshold)
		}
		if step.GoalID != nil && *step.GoalID <= 0 {
			t.Fatalf("parseFunnelStep(%q) accepted goal id %d", in, *step.GoalID)
		}
	})
}

// FuzzParseWeekStart: a wrong week start silently shifts every weekly figure,
// so anything it accepts has to be a day the API knows.
func FuzzParseWeekStart(f *testing.F) {
	for _, seed := range []string{
		"monday", "MONDAY", "mon", "0", "6", "7", "-1", "", " ", "sun day",
		"sunday\n", "١", "3.0", "+2",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		n, err := parseWeekStart(in)
		if err != nil {
			return
		}
		if n < 0 || n > 6 {
			t.Fatalf("parseWeekStart(%q) = %d, outside the 0..6 the API accepts", in, n)
		}
	})
}

// FuzzSettingsGroup: the group becomes part of a URL path, so anything
// accepted has to be one of the five known names and nothing else -- a value
// that slipped through would build a request to a path that does not exist,
// or worse, to one that does.
func FuzzSettingsGroup(f *testing.F) {
	for _, seed := range []string{
		"tracking", "hostnames", "countries", "blocked-ips", "public-dashboard",
		"", "..", "../keys", "tracking/../../keys", "TRACKING", "tracking ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if !validSettingsGroup(in) {
			return
		}
		found := false
		for _, g := range settingsGroups {
			if g == in {
				found = true
			}
		}
		if !found {
			t.Fatalf("validSettingsGroup(%q) accepted a name that is not one of the five", in)
		}
		if strings.ContainsAny(in, "/.%") {
			t.Fatalf("validSettingsGroup(%q) accepted a value that would change the request path", in)
		}
	})
}
