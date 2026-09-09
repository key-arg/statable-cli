// Package query builds and validates a Stats API query before it is sent.
//
// Every rule here mirrors one the server enforces. Checking locally is not
// about trusting the client: it is about telling the user which flag is wrong
// while they still have the command in their shell history, instead of
// relaying a 400 that names a JSON field they never typed.
package query

import "sort"

// Metrics available on aggregates and time series.
var seriesMetrics = map[string]bool{
	"visitors":        true,
	"pageviews":       true,
	"visits":          true,
	"visit_duration":  true,
	"bounce_rate":     true,
	"views_per_visit": true,
	"engagement_time": true,
}

// Metrics that exist only inside a breakdown, and only on dimensions that
// compute them.
var breakdownOnlyMetrics = map[string]bool{
	"events":          true,
	"conversion_rate": true,
	"time_on_page":    true,
	"scroll_depth":    true,
	"exit_rate":       true,
}

// TimeDimensions are the buckets a time series can use.
var TimeDimensions = map[string]bool{
	"time":        true,
	"time:minute": true,
	"time:hour":   true,
	"time:day":    true,
	"time:week":   true,
	"time:month":  true,
}

// breakdownMetrics lists, per dimension, exactly which metrics that dimension
// computes. Asking for one elsewhere fails with 400 rather than returning a
// null column, so the same restriction is applied here.
var breakdownMetrics = map[string]map[string]bool{
	"visit:source":          visitBase,
	"visit:referrer":        visitBase,
	"visit:channel":         visitBase,
	"visit:utm_source":      visitBase,
	"visit:utm_medium":      visitBase,
	"visit:utm_campaign":    visitBase,
	"visit:utm_content":     visitBase,
	"visit:utm_term":        visitBase,
	"visit:country":         visitBase,
	"visit:region":          visitBase,
	"visit:city":            visitBase,
	"visit:browser":         visitBase,
	"visit:browser_version": visitBase,
	"visit:os":              visitBase,
	"visit:os_version":      visitBase,
	"visit:device":          visitBase,

	"visit:entry_page": set("visitors", "visits", "visit_duration"),
	"visit:exit_page":  set("visitors", "visits", "exit_rate"),

	"event:page":   set("visitors", "pageviews", "bounce_rate", "time_on_page", "scroll_depth"),
	"event:folder": set("visitors", "pageviews", "bounce_rate", "time_on_page", "scroll_depth"),
	"event:hostname": set("visitors", "visits", "pageviews", "visit_duration",
		"bounce_rate", "views_per_visit"),
	"event:status_code": set("pageviews"),
	"event:name":        set("visitors", "events"),
	"event:goal":        set("visitors", "events", "conversion_rate"),
}

var visitBase = set("visitors", "visit_duration", "bounce_rate")

// propsMetrics is what event:props:<key> computes. The dimension is dynamic,
// so it cannot sit in the map above, but it still has an allowlist.
var propsMetrics = set("visitors", "events")

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// FilterFields are the fields a filter may name. The dimension prefixes
// (visit:, event:) are dropped here, which is the API's own convention and a
// frequent source of confusion.
var FilterFields = set(
	"browser", "browser_version", "os", "os_version", "device",
	"country", "city", "region", "hostname",
	"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term",
	"entry_page", "exit_page", "source", "channel", "referrer",
	"page", "code", "event",
)

// GeoDimensions return a code as the value and the readable name in a sibling
// labels map. The code is what the matching filter accepts, so a breakdown
// value drops straight into the next query.
var GeoDimensions = set("visit:country", "visit:region", "visit:city")

// KnownMetric reports whether a metric exists at all.
func KnownMetric(m string) bool {
	return seriesMetrics[m] || breakdownOnlyMetrics[m]
}

// SortedKeys is a stable listing, used to suggest alternatives in errors.
func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BreakdownDimensions lists every non-time dimension, including the dynamic
// property dimension, which is easy to leave out of a suggestion list and then
// tell a user that a supported dimension does not exist.
func BreakdownDimensions() []string {
	out := make([]string, 0, len(breakdownMetrics)+1)
	for k := range breakdownMetrics {
		out = append(out, k)
	}
	out = append(out, "event:props:<key>")
	sort.Strings(out)
	return out
}

// AllMetrics is every metric name the API computes on some shape, sorted.
// It is what a shell completion offers: a dimension may reject one of these,
// and the build step says which, but proposing only the aggregate metrics
// would hide half the surface.
func AllMetrics() []string {
	out := make([]string, 0, len(seriesMetrics)+len(breakdownOnlyMetrics))
	for m := range seriesMetrics {
		out = append(out, m)
	}
	for m := range breakdownOnlyMetrics {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
