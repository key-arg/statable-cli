// Package meta holds checks about the repository itself rather than about any
// one package's behaviour.
package meta

// APIEndpoint is one operation the Stats API exposes, and what this CLI does
// about it.
type APIEndpoint struct {
	Method  string
	Path    string
	Command string // the command that covers it, or "" when nothing does
	Why     string // required when Command is empty: why nothing covers it
}

// Coverage is every operation in the v1 API and the decision taken on it.
//
// This list exists because its absence caused the same bug three times. Each
// round I checked the command surface against one document -- endpoints.md,
// then the plan's draft -- and each time a different document turned out to
// hold endpoints neither of them listed. props, funnels and subscription were
// found late; goals and snippet later still; registration last. Every one of
// them had been written down somewhere.
//
// The rule now: an operation is in this table or the build fails. Adding an
// endpoint to the API and forgetting the CLI is still possible, but it can no
// longer be quiet -- see TestEveryAPIEndpointHasADecision and the CI job that
// diffs this against the live specification.
var Coverage = []APIEndpoint{
	// Analytics.
	{"POST", "/query", "query, stats, series, top, check", ""},
	{"GET", "/current-visitors", "now", ""},
	{"GET", "/sites", "sites", ""},
	{"GET", "/props", "props", ""},
	{"GET", "/subscription", "subscription", ""},

	// Funnels.
	{"GET", "/funnels", "funnels", ""},
	{"POST", "/funnels/{id}/report", "funnel", ""},
	{"POST", "/sites/{id}/funnels", "funnels create", ""},
	{"PUT", "/sites/{id}/funnels/{funnelId}", "funnels edit", ""},
	{"DELETE", "/sites/{id}/funnels/{funnelId}", "funnels delete", ""},

	// Goals.
	{"GET", "/sites/{id}/goals", "goals", ""},
	{"POST", "/sites/{id}/goals", "goals create", ""},
	{"PUT", "/sites/{id}/goals/{goalId}", "goals edit", ""},
	{"DELETE", "/sites/{id}/goals/{goalId}", "goals delete", ""},

	// Sites.
	{"POST", "/sites", "sites create", ""},
	{"PATCH", "/sites/{id}", "sites edit", ""},
	{"DELETE", "/sites/{id}", "sites delete", ""},
	{"GET", "/sites/{id}/snippet", "snippet", ""},

	// Settings.
	{"GET", "/sites/{id}/settings/tracking", "settings tracking", ""},
	{"PUT", "/sites/{id}/settings/tracking", "settings set tracking", ""},
	{"GET", "/sites/{id}/settings/hostnames", "settings hostnames", ""},
	{"PUT", "/sites/{id}/settings/hostnames", "settings set hostnames", ""},
	{"GET", "/sites/{id}/settings/countries", "settings countries", ""},
	{"PUT", "/sites/{id}/settings/countries", "settings set countries", ""},
	{"GET", "/sites/{id}/settings/blocked-ips", "settings blocked-ips", ""},
	{"PUT", "/sites/{id}/settings/blocked-ips", "settings set blocked-ips", ""},
	{"GET", "/sites/{id}/settings/public-dashboard", "settings public-dashboard", ""},
	{"PUT", "/sites/{id}/settings/public-dashboard", "settings set public-dashboard", ""},

	// Keys.
	{"GET", "/keys", "keys", ""},
	{"POST", "/keys", "keys create", ""},
	{"DELETE", "/keys/{id}", "keys revoke", ""},
	{"POST", "/keys/{id}/rotate", "keys rotate", ""},
	{"GET", "/keys/{id}/events", "keys events", ""},

	// Registration.
	{"POST", "/auth/send-otp", "auth register", ""},
	{"POST", "/auth/verify-otp", "auth register", ""},
}
