package query

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
)

// Spec is what a command asks for, before it becomes a request body.
type Spec struct {
	SiteID    int64
	Metrics   []string
	DateRange string
	Dimension string
	Filters   []api.Filter
	Compare   string
	Limit     int
	Offset    int
	// HasLimit distinguishes "no limit given" from "--limit 0", because the
	// API rejects either one on an aggregate or a time series.
	HasLimit  bool
	HasOffset bool
}

// maxExplicitDays is the cap query.md puts on an explicit date pair.
const maxExplicitDays = 366

// maxExplicitSpan is the same limit as the server applies it: a difference,
// not a count.
const maxExplicitSpan = time.Duration(maxExplicitDays) * 24 * time.Hour

var (
	daysRange   = regexp.MustCompile(`^([1-9][0-9]{0,2})d$`)
	dateLiteral = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// eventPropsPrefix and isPropsDim mirror the server exactly: it cuts the
// prefix and requires the remainder to be non-empty, nothing more.
//
// A regular expression came close but not close enough. In Go `.` does not
// match a newline and `$` without the multiline flag anchors at the end of the
// text, so a key containing a newline was refused where the API accepts it.
// Unreachable in practice, but the point of mirroring a rule is that it should
// not need a second reading to check.
const eventPropsPrefix = "event:props:"

func isPropsDim(dim string) bool {
	key, ok := strings.CutPrefix(dim, eventPropsPrefix)
	return ok && key != ""
}

// ParseDateRange accepts the presets, "Nd", and an explicit pair written
// either "from..to" or "from,to". It returns the value to send, which is a
// string for a preset and a two-element slice for a pair.
func ParseDateRange(s string) (any, error) {
	v, err := parseDateRange(s)
	return v, usageErr(err)
}

func parseDateRange(s string) (any, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return nil, clierr.Fail("INVALID_RANGE", "no date range was given",
			"statable ... --range 7d")
	case "month", "realtime":
		// Only the presets query.md documents. "today" and "day" used to be
		// accepted here and mapped to "day", which the API answers with 400:
		// the local validator told the user it was fine and the server then
		// disagreed. A single day is an explicit pair.
		return s, nil
	}

	if m := daysRange.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 90 {
			return nil, clierr.Failf("INVALID_RANGE",
				"a relative range covers 1 to 90 days, not %d; for longer, give two dates", n)
		}
		return s, nil
	}

	var parts []string
	switch {
	case strings.Contains(s, ".."):
		parts = strings.SplitN(s, "..", 2)
	case strings.Contains(s, ","):
		parts = strings.SplitN(s, ",", 2)
	}
	if len(parts) == 2 {
		from, to := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !dateLiteral.MatchString(from) || !dateLiteral.MatchString(to) {
			return nil, clierr.Failf("INVALID_RANGE",
				"a date pair looks like 2026-06-01..2026-06-30, not %q", s)
		}
		fromT, err := time.Parse("2006-01-02", from)
		if err != nil {
			return nil, clierr.Failf("INVALID_RANGE", "%s is not a real date", from)
		}
		toT, err := time.Parse("2006-01-02", to)
		if err != nil {
			return nil, clierr.Failf("INVALID_RANGE", "%s is not a real date", to)
		}
		if fromT.After(toT) {
			return nil, clierr.Failf("INVALID_RANGE",
				"the range starts after it ends: %s is later than %s", from, to)
		}
		// The API caps an explicit pair by the *span* between the two dates,
		// not by the number of days it covers: it rejects when to-from is
		// more than 366 days. Counting inclusive days instead rejected a
		// 367-day range the server accepts, so a legal query failed locally
		// with an error the server would never have given.
		if span := toT.Sub(fromT); span > maxExplicitSpan {
			return nil, clierr.Failf("INVALID_RANGE",
				"an explicit range spans at most %d days end to end, and this one spans %d",
				maxExplicitDays, int(span.Hours()/24))
		}
		return []string{from, to}, nil
	}

	return nil, clierr.Failf("INVALID_RANGE",
		"unknown date range %q; use 7d, 30d, month, realtime, Nd, or 2026-06-01..2026-06-30", s)
}

// ParseFilter turns one --filter argument into a Filter.
//
// The operators are chosen so a filter reads like the question it asks:
//
//	country=US        is
//	country!=US       is not
//	page~/blog        contains
//	page!~/admin      does not contain
//
// Several values are separated by commas and OR'd together, which is safe
// because the API forbids a comma inside a value. Separate --filter flags are
// AND'd, so the two levels never collide.
func ParseFilter(s string) (api.Filter, error) {
	f, err := parseFilter(s)
	return f, usageErr(err)
}

func parseFilter(s string) (api.Filter, error) {
	raw := s
	s = strings.TrimSpace(s)

	// The operator is whichever appears first, not whichever is tested first.
	// Scanning by operator instead meant a value containing "~" — a /~user
	// path, say — was read as the contains operator, and the user was told
	// their field was called "page=/".
	type opTok struct {
		tok string
		op  string
	}
	toks := []opTok{{"!~", "does_not_contain"}, {"!=", "is_not"}, {"~", "contains"}, {"=", "is"}}

	best, bestAt := opTok{}, -1
	for _, t := range toks {
		at := strings.Index(s, t.tok)
		if at < 0 {
			continue
		}
		// At the same position the longer token wins, so "!=" is never read
		// as "!" followed by "=".
		if bestAt < 0 || at < bestAt || (at == bestAt && len(t.tok) > len(best.tok)) {
			best, bestAt = t, at
		}
	}
	if bestAt < 0 {
		return api.Filter{}, clierr.Failf(clierr.CodeInvalidFilter,
			"filter %q has no operator; write country=US, page~/blog, or country!=US", raw)
	}
	field, rest, op := s[:bestAt], s[bestAt+len(best.tok):], best.op

	field = strings.TrimSpace(field)
	if field == "" {
		return api.Filter{}, clierr.Failf(clierr.CodeInvalidFilter,
			"filter %q names no field", raw)
	}
	// The dimension prefix is a common slip: the dimension is visit:country,
	// but the filter field is plain country.
	if trimmed := strings.TrimPrefix(strings.TrimPrefix(field, "visit:"), "event:"); trimmed != field {
		if FilterFields[trimmed] {
			return api.Filter{}, clierr.Failf(clierr.CodeInvalidFilter,
				"filter fields drop the dimension prefix: write %s, not %s", trimmed, field)
		}
	}
	if !FilterFields[field] {
		return api.Filter{}, clierr.Failf(clierr.CodeInvalidFilter,
			"unknown filter field %q; known fields are %s",
			field, strings.Join(SortedKeys(FilterFields), ", "))
	}

	values := []string{}
	for _, v := range strings.Split(rest, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return api.Filter{}, clierr.Failf(clierr.CodeInvalidFilter,
			"filter %q has no value", raw)
	}

	// The event filter restricts the whole query to sessions in which that
	// custom event fired, so it takes exactly one value and rejects the two
	// automatic events.
	if field == "event" {
		if op != "is" {
			return api.Filter{}, clierr.Fail(clierr.CodeInvalidFilter,
				"the event filter only supports =, because it scopes the whole query")
		}
		if len(values) > 1 {
			return api.Filter{}, clierr.Fail(clierr.CodeInvalidFilter,
				"the event filter takes exactly one event name")
		}
		if v := values[0]; v == "pageview" || v == "engagement" {
			return api.Filter{}, clierr.Failf(clierr.CodeInvalidFilter,
				"%q is an automatic event and cannot be filtered on", v)
		}
	}

	return api.Filter{Field: field, Operator: op, Values: values}, nil
}

// Build validates the spec and turns it into a request body.
func (s Spec) Build() (*api.QueryRequest, error) {
	req, err := s.build()
	return req, usageErr(err)
}

// usageErr marks a validation failure as a malformed command line.
//
// Every error this package raises is found before anything is sent, which is
// the CLI's own definition of exit 64. They used to exit 2, the code reserved
// for "something broke", so a script could not tell a typo in a metric name
// from a server outage — and the limit and funnel-id checks already exited 64,
// which made the split arbitrary as well as wrong.
func usageErr(err error) error {
	var e *clierr.Err
	if errors.As(err, &e) && e.Exit == 0 {
		e.Exit = clierr.ExitUsage
	}
	return err
}

func (s Spec) build() (*api.QueryRequest, error) {
	if len(s.Metrics) == 0 {
		return nil, clierr.Fail("METRICS_REQUIRED", "no metric was asked for",
			"statable ... --metric visitors")
	}

	isBreakdown := s.Dimension != "" && !TimeDimensions[s.Dimension]

	for _, m := range s.Metrics {
		if !KnownMetric(m) {
			// The list has to match the shape being asked for: offering
			// engagement_time for a page breakdown, where it does not exist,
			// while hiding time_on_page, where it does, is worse than silence.
			available := seriesMetrics
			if isBreakdown {
				if allowed, known := breakdownMetrics[s.Dimension]; known {
					available = allowed
				} else if isPropsDim(s.Dimension) {
					available = propsMetrics
				}
			}
			return nil, clierr.Failf("UNKNOWN_METRIC",
				"unknown metric %q; %s computes %s",
				m, describeShape(s.Dimension), strings.Join(SortedKeys(available), ", "))
		}
		if !isBreakdown && breakdownOnlyMetrics[m] {
			return nil, clierr.Failf("METRIC_NOT_AVAILABLE",
				"%s only exists inside a breakdown, so it needs a dimension", m)
		}
		if isBreakdown {
			allowed, known := breakdownMetrics[s.Dimension]
			if !known && isPropsDim(s.Dimension) {
				allowed, known = propsMetrics, true
			}
			if known && !allowed[m] {
				return nil, clierr.Failf("METRIC_NOT_AVAILABLE",
					"%s does not compute %s; it computes %s",
					s.Dimension, m, strings.Join(SortedKeys(allowed), ", "))
			}
		}
	}

	if s.Dimension != "" && !TimeDimensions[s.Dimension] {
		if _, known := breakdownMetrics[s.Dimension]; !known && !isPropsDim(s.Dimension) {
			return nil, clierr.Failf("UNKNOWN_DIMENSION",
				"unknown dimension %q; available are %s and the time buckets",
				s.Dimension, strings.Join(BreakdownDimensions(), ", "))
		}
	}

	// event:props:<key> belongs to a single custom event, so it needs an
	// event filter naming that event.
	if isPropsDim(s.Dimension) {
		hasEvent := false
		for _, f := range s.Filters {
			if f.Field == "event" {
				hasEvent = true
			}
		}
		if !hasEvent {
			return nil, clierr.Failf("EVENT_FILTER_REQUIRED",
				"%s needs an event filter naming the event that records it",
				s.Dimension)
		}
	}

	// These three are command-line mistakes: nothing is sent, and the exit
	// code says so. The server would silently clamp a limit outside the range
	// and a negative offset to zero, which is worse for a person: asking for
	// 5000 rows and being handed 1000 without a word looks like the site only
	// has 1000.
	if (s.HasLimit || s.HasOffset) && !isBreakdown {
		return nil, clierr.Fail("LIMIT_OFFSET_MISUSE",
			"limit and offset apply to a breakdown only, not to totals or a series").
			WithExit(clierr.ExitUsage)
	}
	if s.HasLimit && (s.Limit < 1 || s.Limit > 1000) {
		return nil, clierr.Failf("LIMIT_OFFSET_MISUSE",
			"limit must be between 1 and 1000, got %d; the API clamps anything outside that, "+
				"so asking for more would quietly return 1000", s.Limit).
			WithExit(clierr.ExitUsage)
	}
	if s.HasOffset && s.Offset < 0 {
		return nil, clierr.Failf("LIMIT_OFFSET_MISUSE",
			"offset counts rows to skip and cannot be negative, got %d", s.Offset).
			WithExit(clierr.ExitUsage)
	}

	dr, err := ParseDateRange(s.DateRange)
	if err != nil {
		return nil, err
	}

	if s.Dimension != "" && TimeDimensions[s.Dimension] {
		if err := checkInterval(s.Dimension, dr); err != nil {
			return nil, err
		}
	}

	req := &api.QueryRequest{
		SiteID:    s.SiteID,
		Metrics:   s.Metrics,
		DateRange: dr,
		Filters:   s.Filters,
	}
	if s.Dimension != "" {
		req.Dimensions = []string{s.Dimension}
	}
	if s.HasLimit {
		l := s.Limit
		req.Limit = &l
	}
	if s.HasOffset {
		o := s.Offset
		req.Offset = &o
	}
	if s.Compare != "" {
		c, err := parseCompare(s.Compare)
		if err != nil {
			return nil, err
		}
		if err := checkCompareLength(dr, c); err != nil {
			return nil, err
		}
		req.Compare = c
	}
	return req, nil
}

func parseCompare(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "previous_period" || s == "previous" {
		return "previous_period", nil
	}
	v, err := ParseDateRange(s)
	if err != nil {
		return nil, clierr.Failf("INVALID_COMPARE",
			"compare takes previous_period or a date pair, not %q", s)
	}
	if pair, ok := v.([]string); ok {
		return pair, nil
	}
	return nil, clierr.Failf("INVALID_COMPARE",
		"a named compare window has to be a date pair, not %q", s)
}

// checkCompareLength enforces the rule the server states plainly: a comparison
// must cover the same span as the range it is compared with.
//
// It can only be checked here when both sides are explicit pairs, which is
// exactly the case the user typed and can fix. A preset resolves server-side,
// and guessing at its length would refuse legal queries.
func checkCompareLength(main, compare any) error {
	if preset, ok := main.(string); ok {
		return checkPresetCompare(preset, compare)
	}
	mp, ok := main.([]string)
	if !ok || len(mp) != 2 {
		return nil
	}
	cp, ok := compare.([]string)
	if !ok || len(cp) != 2 {
		return nil
	}
	span := func(p []string) (time.Duration, bool) {
		from, err1 := time.Parse("2006-01-02", p[0])
		to, err2 := time.Parse("2006-01-02", p[1])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		return to.Sub(from), true
	}
	ms, ok1 := span(mp)
	cs, ok2 := span(cp)
	if !ok1 || !ok2 || ms == cs {
		return nil
	}
	days := func(d time.Duration) int { return int(d.Hours()/24) + 1 }
	return clierr.Failf("INVALID_COMPARE",
		"a comparison has to cover the same number of days as the range: "+
			"--range spans %d and --compare spans %d",
		days(ms), days(cs))
}

// openEndedPreset reports whether a preset's window ends at the current
// instant rather than at a midnight.
//
// Every Nd form and realtime do: the server builds them with no end date, so
// the end is read from the clock at query time (params_ranges.go). Their span
// is therefore never a whole number of days, and an explicit compare pair,
// whose span always is, can never match it. "month" is not in this set — the
// server gives it a real end — so it is left alone.
func openEndedPreset(s string) bool {
	if s == "realtime" {
		return true
	}
	return daysRange.MatchString(s)
}

// checkPresetCompare refuses the one combination the server rejects every
// time. Sending it anyway spends a call from an hourly budget to be told
// compare_length_mismatch, and the message names neither flag the user typed.
func checkPresetCompare(preset string, compare any) error {
	pair, ok := compare.([]string)
	if !ok || len(pair) != 2 || !openEndedPreset(preset) {
		return nil
	}
	return clierr.Failf("INVALID_COMPARE",
		"--range %s ends at this moment, not at a midnight, so no fixed pair of dates "+
			"can cover the same span; use --compare previous_period, or give --range an "+
			"explicit pair too", preset)
}

// DescribeRange renders what the server said it actually used.
func DescribeRange(pair []string) string {
	switch len(pair) {
	case 1:
		return pair[0]
	case 2:
		if pair[0] == pair[1] {
			return pair[0]
		}
		return fmt.Sprintf("%s to %s", pair[0], pair[1])
	}
	return ""
}

// describeShape names the query shape for an error message.
func describeShape(dim string) string {
	switch {
	case dim == "":
		return "a totals query"
	case TimeDimensions[dim]:
		return "a time series"
	default:
		return dim
	}
}

// bucketsFor lists the time buckets a range supports, mirroring the server's
// own table. Only the minute case was checked before, and only in one
// direction, so `--range realtime` with the default daily bucket passed
// validation and came back as a 400 — including from the example the `now`
// command's own help printed.
func bucketsFor(dr any) (map[string]bool, string) {
	switch v := dr.(type) {
	case string:
		switch {
		case v == "realtime":
			return set("minute"), "realtime"
		case v == "month":
			return set("day", "week", "month"), "a calendar month"
		case v == "1d":
			return set("minute", "hour"), "a single day"
		}
		if m := daysRange.FindStringSubmatch(v); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n > 30 {
				return set("day", "week", "month"), v
			}
			return set("day", "week", "hour"), v
		}
	case []string:
		// An explicit pair is the server's "custom" bucket.
		return set("day", "week", "quarter"), "an explicit date range"
	}
	return nil, ""
}

func checkInterval(dim string, dr any) error {
	bucket := strings.TrimPrefix(dim, "time:")
	if bucket == "time" || bucket == "" {
		bucket = "day"
	}
	allowed, what := bucketsFor(dr)
	if allowed == nil {
		return nil
	}
	if allowed[bucket] {
		return nil
	}
	return clierr.Failf("INVALID_INTERVAL",
		"%s does not support %s buckets; it supports %s",
		what, bucket, strings.Join(SortedKeys(allowed), ", "))
}
