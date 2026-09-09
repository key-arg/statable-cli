package cli

import (
	"encoding/json"
	"fmt"
	"time"
)

// shortTimestamp renders an RFC 3339 instant for a person.
//
// The API sends nanosecond precision, and "2026-06-01T10:00:00.123456789Z" is
// nine digits nobody reads in a column they are scanning for a date. A value
// that does not parse is passed through untouched rather than guessed at.
func shortTimestamp(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// dateRangeParam renders a parsed date range for a query string.
//
// ParseDateRange returns either a string preset or the two-element pair the
// JSON body wants. The GET endpoints take the same value on the query string,
// and the server reads it one of two ways: a bare token is wrapped as a JSON
// string, and anything starting with "[" is parsed as JSON. So a pair has to
// travel as ["from","to"], not as the "from..to" a person types. Sending the
// dotted form was rejected as invalid_date_range, since it is neither a
// recognised preset nor an array.
func dateRangeParam(dr any) string {
	if s, ok := dr.(string); ok {
		return s
	}
	b, err := json.Marshal(dr)
	if err != nil {
		return fmt.Sprint(dr)
	}
	return string(b)
}
