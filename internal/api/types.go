package api

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Site is one entry from GET /sites.
type Site struct {
	SiteID         int64          `json:"site_id"`
	Name           string         `json:"name"`
	Timezone       string         `json:"timezone"`
	Hash           string         `json:"hash"`
	StatsStartDate *string        `json:"stats_start_date"`
	CreatedAt      string         `json:"created_at"`
	Stats          *SiteStats     `json:"stats,omitempty"`
	Extra          map[string]any `json:"-"`
}

// SiteStats is attached to a site only when /sites was asked for a date range.
type SiteStats struct {
	DateRange string         `json:"date_range"`
	Metrics   map[string]any `json:"metrics"`
}

// SitesResponse is the body of GET /sites.
type SitesResponse struct {
	Sites []Site `json:"sites"`
}

// CurrentVisitors is the body of GET /current-visitors.
type CurrentVisitors struct {
	SiteID   int64 `json:"site_id"`
	Visitors int64 `json:"visitors"`
}

// Filter narrows a query. Values are OR'd together; separate filters are AND'd.
type Filter struct {
	Field    string   `json:"field"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

// QueryRequest is the body of POST /query.
//
// SiteID is omitted when zero so a single-site key can leave it out, which the
// API allows. Limit and Offset are pointers because zero is a meaningful
// offset and because sending either one on an aggregate or a time series is
// rejected with limit_offset_misuse.
type QueryRequest struct {
	SiteID     int64    `json:"site_id,omitempty"`
	Metrics    []string `json:"metrics"`
	DateRange  any      `json:"date_range"`
	Dimensions []string `json:"dimensions,omitempty"`
	Filters    []Filter `json:"filters,omitempty"`
	Compare    any      `json:"compare,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
	Offset     *int     `json:"offset,omitempty"`
}

// CompareInfo is the previous-period figure for one metric, and the percent
// difference from it.
//
// The field is "change", not "diff". The API's own internal row type spells it
// diff, and reading that struct instead of the v1 handler's output made every
// comparison decode as zero — silently, at exit 0, on a query the caller paid
// extra for. The fake server in the tests was written from the same misreading,
// so nothing caught it. Confirmed against the handler, which writes "change" in
// all three places it emits a comparison.
type CompareInfo struct {
	Value  float64 `json:"value"`
	Change float64 `json:"change"`
}

// Result is one row. Which fields are populated depends on the query shape:
// an aggregate has metrics only, a time series adds one time dimension, a
// breakdown adds a dimension and, for the geo dimensions, a label.
type Result struct {
	Dimensions map[string]string      `json:"dimensions,omitempty"`
	Labels     map[string]string      `json:"labels,omitempty"`
	Metrics    map[string]any         `json:"metrics"`
	Compare    map[string]CompareInfo `json:"compare,omitempty"`
}

// Meta is pagination information, present on breakdowns only.
type Meta struct {
	Total   int64 `json:"total"`
	Limit   int   `json:"limit"`
	Offset  int   `json:"offset"`
	HasMore bool  `json:"has_more"`
}

// QueryResponse is the body of POST /query.
type QueryResponse struct {
	Results []Result `json:"results"`
	Meta    *Meta    `json:"meta,omitempty"`
	// Query echoes what the server resolved, including date_range as concrete
	// dates, so a caller never has to reproduce the calendar arithmetic.
	Query json.RawMessage `json:"query,omitempty"`
	// HasImport is true when the window overlaps GA4-imported data, meaning
	// the numbers blend imported and native traffic.
	HasImport bool `json:"has_import,omitempty"`
}

// ResolvedRange digs the concrete dates the server used out of the echo.
func (q *QueryResponse) ResolvedRange() []string {
	if len(q.Query) == 0 {
		return nil
	}
	var echo struct {
		DateRange json.RawMessage `json:"date_range"`
	}
	if err := json.Unmarshal(q.Query, &echo); err != nil {
		return nil
	}
	var pair []string
	if err := json.Unmarshal(echo.DateRange, &pair); err == nil {
		return pair
	}
	var single string
	if err := json.Unmarshal(echo.DateRange, &single); err == nil {
		return []string{single}
	}
	return nil
}

// ResolvedCompareRange digs out the dates the comparison actually used.
//
// The server resolves "previous_period" into concrete dates and echoes them,
// and until now the CLI printed the main period and left the reader to guess
// what a change of -12% was measured against. A comparison whose baseline is
// invisible is a number nobody can check.
func (q *QueryResponse) ResolvedCompareRange() []string {
	if len(q.Query) == 0 {
		return nil
	}
	var echo struct {
		CompareDateRange []string `json:"compare_date_range"`
	}
	if err := json.Unmarshal(q.Query, &echo); err != nil {
		return nil
	}
	return echo.CompareDateRange
}

// Num reads a metric as a float. Metric values arrive as JSON numbers, but a
// rate may be null when it is undefined for the window.
func Num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// PropKey is one entry of GET /props.
//
// A key belongs to the event it was sent with, so the same name under two
// events is two entries. Both halves are needed downstream: the event as a
// filter, the key as the dimension of the next breakdown.
type PropKey struct {
	Key       string `json:"key"`
	Event     string `json:"event"`
	Count     int64  `json:"count"`
	FirstSeen string `json:"first_seen"`
}

// PropsResponse is the GET /props envelope.
type PropsResponse struct {
	Props []PropKey `json:"props"`
}

// FunnelStep is one step of a saved definition. Which fields are set depends
// on Kind, which is why every optional one is a pointer: an absent path and an
// empty path are different things when the step is being read back.
type FunnelStep struct {
	Kind      string  `json:"kind"`
	GoalID    *int64  `json:"goal_id,omitempty"`
	Path      *string `json:"path,omitempty"`
	Operator  *string `json:"operator,omitempty"`
	Event     *string `json:"event,omitempty"`
	PropKey   *string `json:"prop_key,omitempty"`
	PropValue *string `json:"prop_value,omitempty"`
	Threshold *int    `json:"threshold,omitempty"`
}

// Funnel is a saved funnel definition from GET /funnels.
type Funnel struct {
	ID          int64        `json:"id"`
	SiteID      int64        `json:"site_id"`
	Name        string       `json:"name"`
	Steps       []FunnelStep `json:"steps"`
	Scope       string       `json:"scope"`
	StrictOrder bool         `json:"strict_order"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

// FunnelsResponse is the GET /funnels envelope.
type FunnelsResponse struct {
	Funnels []Funnel `json:"funnels"`
}

// FunnelReportRequest is the POST /funnels/{id}/report body.
type FunnelReportRequest struct {
	SiteID    int64    `json:"site_id,omitempty"`
	DateRange any      `json:"date_range"`
	Filters   []Filter `json:"filters,omitempty"`
}

// FunnelReportStep is one row of a funnel result. The order is the server's
// and must not be re-sorted: index is the position from zero.
type FunnelReportStep struct {
	Index          int     `json:"index"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	Visitors       int64   `json:"visitors"`
	ConversionRate float64 `json:"conversion_rate"`
	Dropoff        int64   `json:"dropoff"`
}

// FunnelReport is the POST /funnels/{id}/report response.
//
// ConversionRate on each step is cumulative against Entering, not against the
// step before, and Dropoff is the opposite: it counts against the previous
// step alone. AllVisitors is the site's total for the period and says how
// large a slice the funnel represents; it is not a funnel figure.
type FunnelReport struct {
	Funnel struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Scope       string `json:"scope"`
		StrictOrder bool   `json:"strict_order"`
	} `json:"funnel"`
	Entering    int64              `json:"entering"`
	AllVisitors int64              `json:"all_visitors"`
	Steps       []FunnelReportStep `json:"steps"`
	Query       map[string]any     `json:"query"`
}

// Subscription is GET /subscription: the plan state of the key's owner.
//
// It takes no site: the answer is about the account. An account holding only
// hobby sites has no subscription row and answers status "none" with
// only_hobby true, which is "free plan", not "no account".
type Subscription struct {
	Status    string  `json:"status"`
	IsTrial   bool    `json:"is_trial"`
	EndsAt    *string `json:"ends_at,omitempty"`
	OnlyHobby bool    `json:"only_hobby"`
}

// Goal is one saved conversion goal from GET /sites/{id}/goals.
//
// SiteID reads the server's `host_id`, not `site_id`. The endpoint returns the
// storage struct directly rather than a v1 shape, so this is the one place in
// the API where the site is not called site_id. Naming the Go field SiteID and
// tagging it host_id keeps the CLI consistent without misreading the wire.
//
// Which of Path, EventName and ScrollDepth is set depends on what kind of goal
// it is, so all three are pointers: an absent path and an empty one are
// different things.
type Goal struct {
	ID          int64   `json:"id"`
	SiteID      int64   `json:"host_id"`
	Name        string  `json:"name"`
	Path        *string `json:"path,omitempty"`
	Operator    string  `json:"operator,omitempty"`
	EventName   *string `json:"event_name,omitempty"`
	ScrollDepth *int    `json:"scroll_depth,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Kind names what the goal matches on, which the server leaves implicit in
// which field it filled.
func (g Goal) Kind() string {
	switch {
	case g.EventName != nil:
		return "event"
	case g.ScrollDepth != nil:
		return "scroll"
	case g.Path != nil:
		return "page"
	}
	return ""
}

// Target is the value the goal matches, whatever kind it is.
func (g Goal) Target() string {
	switch {
	case g.EventName != nil:
		return *g.EventName
	case g.ScrollDepth != nil:
		return strconv.Itoa(*g.ScrollDepth) + "%"
	case g.Path != nil:
		return *g.Path
	}
	return ""
}

// GoalsResponse is the GET /sites/{id}/goals envelope.
type GoalsResponse struct {
	Goals []Goal `json:"goals"`
}

// Snippet is GET /sites/{id}/snippet: the tag to put on a page.
type Snippet struct {
	SiteID    int64  `json:"site_id"`
	ScriptURL string `json:"script_url"`
	Snippet   string `json:"snippet"`
}

// APIKey is one entry of GET /keys. The secret itself is never returned: only
// Prefix, which is enough to tell two keys apart in a listing.
//
// WebsiteID is nil for a key that reaches every site the account owns, and
// ExpiresAt is nil for a key that never expires. Both are pointers because
// "all sites" and "site zero" are different answers, as are "never" and "the
// zero time".
type APIKey struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	WebsiteID  *int64  `json:"website_id"`
	Scopes     string  `json:"scopes"`
	CreatedAt  string  `json:"created_at"`
	CreatedIP  *string `json:"created_ip"`
	LastUsedAt *string `json:"last_used_at"`
	LastUsedIP *string `json:"last_used_ip"`
	ExpiresAt  *string `json:"expires_at"`
}

// Scope reports whether the key carries one named permission.
func (k APIKey) Scope(want string) bool {
	for _, s := range strings.Split(k.Scopes, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// KeysResponse is the GET /keys envelope.
type KeysResponse struct {
	Keys []APIKey `json:"keys"`
}

// KeyEvent is one line of a key's audit trail.
//
// There is no id: the server sends event, ip, user_agent, created_at and the
// key that performed the action. A column reading an absent field printed 0 on
// every row, which looks like data and is not.
type KeyEvent struct {
	Event      string  `json:"event"`
	IP         *string `json:"ip"`
	UserAgent  *string `json:"user_agent"`
	CreatedAt  string  `json:"created_at"`
	ActorKeyID *int64  `json:"actor_key_id"`
}

// KeyEventsResponse is the GET /keys/{id}/events envelope.
type KeyEventsResponse struct {
	Events []KeyEvent `json:"events"`
}

// TrackingFeature is one entry of the tracking catalogue: what the script can
// collect, and whether this site has it on.
type TrackingFeature struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Enabled  bool     `json:"enabled"`
	Locked   bool     `json:"locked"`
	Default  bool     `json:"default"`
	Requires []string `json:"requires,omitempty"`
	SizeBr   int      `json:"size_br,omitempty"`
}

// TrackingSettings is GET /sites/{id}/settings/tracking.
//
// Three of these fields were wrong in the first version, and every one of them
// was wrong in the same direction: the shape I expected was simpler than the
// shape the server sends. version is a number, enabled is the list of feature
// ids that are on, and features are objects rather than names. json.Unmarshal
// stops at the first mismatch, so the command could never decode a real
// response -- and because tracking is first in the group list, plain
// `statable settings` died before printing the four groups that did work.
type TrackingSettings struct {
	SiteID   int64             `json:"site_id"`
	Version  int               `json:"version"`
	Bundle   string            `json:"bundle"`
	Enabled  []string          `json:"enabled"`
	Features []TrackingFeature `json:"features"`
}

// HostnameSettings and CountrySettings share a shape: two lists, either of
// which may be empty. An empty allow list means "everything not blocked".
type HostnameSettings struct {
	Allowed []string `json:"allowed"`
	Blocked []string `json:"blocked"`
}

// CountryEntry is one country on a list, with the time it was added.
type CountryEntry struct {
	Code      string `json:"code"`
	CreatedAt string `json:"created_at"`
}

// CountrySettings is GET /sites/{id}/settings/countries.
//
// Not the same shape as hostnames, despite reading like it. The write takes
// bare ISO codes and the read returns an entry per country, and the asymmetry
// is deliberate on the server's side. Decoding the read as []string failed on
// any site that actually had a country listed -- which is the only case worth
// reading.
type CountrySettings struct {
	Allowed []CountryEntry `json:"allowed"`
	Blocked []CountryEntry `json:"blocked"`
}

// Codes flattens a list of entries to the codes a filter accepts.
func Codes(in []CountryEntry) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		out = append(out, e.Code)
	}
	return out
}

// CountryListRequest is the PUT body: bare codes, not entries.
type CountryListRequest struct {
	Allowed []string `json:"allowed"`
	Blocked []string `json:"blocked"`
}

// BlockedIPSettings is GET /sites/{id}/settings/blocked-ips.
type BlockedIPSettings struct {
	BlockedIPs []string `json:"blocked_ips"`
}

// PublicDashboardSettings is whether the site's stats are world-readable.
type PublicDashboardSettings struct {
	SiteID  int64 `json:"site_id"`
	Enabled bool  `json:"enabled"`
}

// CreateSiteRequest is POST /sites.
type CreateSiteRequest struct {
	URL      string `json:"url"`
	Timezone string `json:"timezone,omitempty"`
	Hobby    bool   `json:"hobby,omitempty"`
}

// CreatedSite is what POST /sites answers with: the site plus the tag to
// install, so a caller never has to ask twice.
type CreatedSite struct {
	SiteID    int64  `json:"site_id"`
	Name      string `json:"name"`
	Timezone  string `json:"timezone"`
	Hash      string `json:"hash"`
	ScriptURL string `json:"script_url"`
	Snippet   string `json:"snippet"`
}

// PatchSiteRequest is PATCH /sites/{id}. Every field is a pointer because a
// patch says what changes, and "set the week to Sunday" is zero.
type PatchSiteRequest struct {
	URL       *string `json:"url,omitempty"`
	Timezone  *string `json:"timezone,omitempty"`
	WeekStart *int    `json:"week_start,omitempty"`
}

// CreateKeyRequest is POST /keys.
type CreateKeyRequest struct {
	Scopes        []string `json:"scopes,omitempty"`
	Name          string   `json:"name"`
	WebsiteID     *int64   `json:"website_id,omitempty"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

// CreatedKey carries the one and only time the secret is ever returned.
//
// The secret field is `token`, not `key`. The server embeds the whole APIKey
// struct and adds `token` beside it, and a field tagged `key` decodes to an
// empty string -- which for this response means the secret is gone, because
// there is no second chance to read it. Same shape of mistake as reading
// `diff` where the server writes `change`, with a worse consequence.
type CreatedKey struct {
	APIKey
	Token string `json:"token"`
}

// Scopes the API recognises, in the order a person would grant them.
var KeyScopes = []string{"read", "sites:write", "keys:manage", "billing:write"}

// CreateGoalRequest is POST /sites/{id}/goals, and the body PUT takes too.
type CreateGoalRequest struct {
	Name        string  `json:"name"`
	Path        *string `json:"path,omitempty"`
	Operator    string  `json:"operator,omitempty"`
	EventName   *string `json:"event_name,omitempty"`
	ScrollDepth *int    `json:"scroll_depth,omitempty"`
}

// CreateFunnelRequest is POST /sites/{id}/funnels and the PUT body.
type CreateFunnelRequest struct {
	Name        string       `json:"name"`
	Steps       []FunnelStep `json:"steps"`
	Scope       string       `json:"scope,omitempty"`
	StrictOrder bool         `json:"strict_order,omitempty"`
}

// SendOTPRequest and VerifyOTPRequest register an account without a browser.
type SendOTPRequest struct {
	Email string `json:"email"`
}

// VerifyOTPResponse is what POST /auth/verify-otp answers with.
//
// The key is nested under "key", not flattened beside the token. Decoding it
// flat left id, prefix and scopes at their zero values while the token itself
// happened to line up, so the command looked like it worked.
type VerifyOTPResponse struct {
	Token   string `json:"token"`
	Key     APIKey `json:"key"`
	Created bool   `json:"created"`
}

// VerifyOTPRequest exchanges the emailed code for a first API key.
type VerifyOTPRequest struct {
	Email         string   `json:"email"`
	Code          string   `json:"code"`
	AcceptTerms   bool     `json:"accept_terms"`
	KeyName       string   `json:"key_name,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}
