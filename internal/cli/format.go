package cli

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/query"
)

// rateMetrics are percentages. They are formatted with a unit so a person
// does not read 41.2 as a count.
var rateMetrics = map[string]bool{
	"bounce_rate":     true,
	"conversion_rate": true,
	"exit_rate":       true,
	"scroll_depth":    true,
}

// durationMetrics are seconds.
var durationMetrics = map[string]bool{
	"visit_duration":  true,
	"engagement_time": true,
	"time_on_page":    true,
}

// formatMetric renders one metric for a person. Machine output never goes
// through here: a script wants the raw number, not "2m 41s".
func formatMetric(name string, v any) string {
	f, ok := api.Num(v)
	if !ok || math.IsNaN(f) {
		return "—"
	}
	switch {
	case rateMetrics[name]:
		// A rate is a percentage, so anything outside a sane range is a
		// server-side anomaly rather than a number to spell out in full:
		// %f on 1e308 produces a three-hundred-digit line.
		if math.Abs(f) > 1e6 {
			return strconv.FormatFloat(f, 'g', 4, 64) + "%"
		}
		return strconv.FormatFloat(f, 'f', 1, 64) + "%"
	case durationMetrics[name]:
		return formatDuration(f)
	case name == "views_per_visit":
		return strconv.FormatFloat(f, 'f', 2, 64)
	default:
		if math.Abs(f) > 1e15 {
			return strconv.FormatFloat(f, 'g', 4, 64)
		}
		return groupThousands(int64(math.Round(f)))
	}
}

func formatDuration(seconds float64) string {
	if seconds < 0 || math.IsInf(seconds, 0) {
		return "—"
	}
	total := int64(math.Round(seconds))
	switch {
	case total < 60:
		return fmt.Sprintf("%ds", total)
	case total < 3600:
		return fmt.Sprintf("%dm %02ds", total/60, total%60)
	default:
		return fmt.Sprintf("%dh %02dm", total/3600, (total%3600)/60)
	}
}

// groupThousands makes a large count readable. It uses a thin space rather
// than a comma so a human-format line stays safe to paste into a CSV cell.
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) <= 4 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// rawMetric renders a metric for a machine: the number as it arrived, with no
// units, no grouping and no rounding.
func rawMetric(v any) string {
	f, ok := api.Num(v)
	if !ok {
		return ""
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// formatDiff renders a period-over-period change. The sign is always shown,
// because "12%" and "+12%" mean different things at a glance.
func formatDiff(d float64) string {
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return ""
	}
	return fmt.Sprintf("%+.1f%%", d)
}

// numeric exposes the API's number coercion to this package.
func numeric(v any) (float64, bool) { return api.Num(v) }

// compareSuffixes name the extra columns and fields that carry a
// period-over-period result into machine output.
//
// Without them the compare was human-only: the request was made and paid for,
// and every scripted caller got the current figure alone. query.md calls the
// per-row compare "the valuable one", so dropping it for scripts threw away
// the reason to ask.
const (
	previousSuffix = "_previous"
	changeSuffix   = "_change"
)

// compareColumns returns the extra column names for one metric.
func compareColumns(metric string) []string {
	return []string{metric + previousSuffix, metric + changeSuffix}
}

// compareCells renders the previous value and the percent change for machine
// output. Absent compare data yields two empty cells so every row keeps the
// same width.
func compareCells(c api.CompareInfo, present bool) []string {
	if !present {
		return []string{"", ""}
	}
	return []string{rawMetric(c.Value), rawMetric(c.Change)}
}

// humanWithCompare appends the change to a formatted value.
func humanWithCompare(formatted string, c api.CompareInfo, present bool) string {
	if !present {
		return formatted
	}
	if d := formatDiff(c.Change); d != "" {
		return formatted + "  " + d
	}
	return formatted
}

// dimensionKey is the column name a dimension gets in output. It is derived
// from the dimension itself so the same request always produces the same key,
// whichever spelling or alias the user reached it by.
func dimensionKey(dim string) string {
	// Every time bucket answers to one key. "time" is documented as an alias
	// of "time:day", and a script grouping by week should not have to know a
	// different key from one grouping by day.
	if query.TimeDimensions[dim] {
		return "time"
	}
	if strings.HasPrefix(dim, "event:props:") {
		return strings.TrimPrefix(dim, "event:props:")
	}
	d := strings.TrimPrefix(strings.TrimPrefix(dim, "visit:"), "event:")
	if d == "" {
		return "value"
	}
	return d
}

// dimensionValue reads a row's dimension value.
//
// The bare "time" dimension is an alias of "time:day", and either side of that
// alias may appear: the client can ask for one and the server answer with the
// other. Looking under a single hard-coded key left every bucket label empty
// for `--dimension time`, in all three formats, with a zero exit code.
func dimensionValue(dims map[string]string, asked string) string {
	if v, ok := dims[asked]; ok && v != "" {
		return v
	}
	if !query.TimeDimensions[asked] {
		return ""
	}
	for name := range query.TimeDimensions {
		if v, ok := dims[name]; ok && v != "" {
			return v
		}
	}
	return ""
}
