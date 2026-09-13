// Package meta holds checks about the repository itself rather than about any
// one package's behaviour.
package meta

import (
	"fmt"
	"strings"
)

// Validate reports what is wrong with a coverage table, as one message per
// problem. It takes the table as an argument rather than reading the package
// variable so a test can hand it a broken one: a check that can only ever see
// a correct table cannot be shown to work.
func Validate(table []APIEndpoint) []string {
	var problems []string
	if len(table) == 0 {
		return []string{"the coverage table is empty"}
	}
	seen := map[string]bool{}
	for _, e := range table {
		key := e.Method + " " + e.Path
		if e.Method == "" || e.Path == "" {
			problems = append(problems, fmt.Sprintf("%+v has no method or path", e))
			continue
		}
		if seen[key] {
			problems = append(problems, key+" appears twice")
		}
		seen[key] = true

		hasWhy := strings.TrimSpace(e.Why) != ""
		switch {
		case e.Command == "" && !hasWhy:
			problems = append(problems,
				key+" has no command and no reason; decide one or the other")
		case e.Command != "" && hasWhy:
			problems = append(problems,
				key+" has both a command and a reason not to have one")
		}
	}
	return problems
}

// APIEndpoint is one operation the Stats API exposes, and what this CLI does
// about it.
type APIEndpoint struct {
	Method  string
	Path    string
	Command string // the command that covers it, or "" when nothing does
	Why     string // required when Command is empty: why nothing covers it
	// NotInSpec marks a route the server really serves but openapi.yaml does
	// not describe. The specification is hand-maintained and the router is
	// not, so the two disagree; without this the coverage check would report
	// such an entry as stale and be wrong.
	NotInSpec bool
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
	{"POST", "/query", "query, stats, series, top, check", "", false},
	{"GET", "/current-visitors", "now", "", false},
	{"GET", "/sites", "sites", "", false},
	{"GET", "/props", "props", "", false},
	{"GET", "/subscription", "subscription", "", false},

	// Funnels.
	{"GET", "/funnels", "funnels", "", false},
	{"POST", "/funnels/{id}/report", "funnel", "", false},
	{"POST", "/sites/{id}/funnels", "funnels create", "", false},
	{"PUT", "/sites/{id}/funnels/{funnelId}", "funnels edit", "", false},
	{"DELETE", "/sites/{id}/funnels/{funnelId}", "funnels delete", "", false},

	// Goals.
	{"GET", "/sites/{id}/goals", "goals", "", false},
	{"POST", "/sites/{id}/goals", "goals create", "", false},
	{"PUT", "/sites/{id}/goals/{goalId}", "goals edit", "", false},
	{"DELETE", "/sites/{id}/goals/{goalId}", "goals delete", "", false},

	// Sites.
	{"POST", "/sites", "sites create", "", false},
	{"PATCH", "/sites/{id}", "sites edit", "", false},
	{"DELETE", "/sites/{id}", "sites delete", "", false},
	{"GET", "/sites/{id}/snippet", "snippet", "", false},

	// Settings.
	{"GET", "/sites/{id}/settings/tracking", "settings tracking", "", false},
	{"PUT", "/sites/{id}/settings/tracking", "settings set tracking", "", false},
	{"GET", "/sites/{id}/settings/hostnames", "settings hostnames", "", false},
	{"PUT", "/sites/{id}/settings/hostnames", "settings set hostnames", "", false},
	{"GET", "/sites/{id}/settings/countries", "settings countries", "", false},
	{"PUT", "/sites/{id}/settings/countries", "settings set countries", "", false},
	{"GET", "/sites/{id}/settings/blocked-ips", "settings blocked-ips", "", false},
	{"PUT", "/sites/{id}/settings/blocked-ips", "settings set blocked-ips", "", false},
	{"GET", "/sites/{id}/settings/public-dashboard", "settings public-dashboard", "", false},
	{"PUT", "/sites/{id}/settings/public-dashboard", "settings set public-dashboard", "", false},

	// Keys.
	{"GET", "/keys", "keys", "", false},
	{"POST", "/keys", "keys create", "", false},
	{"DELETE", "/keys/{id}", "keys revoke", "", false},
	{"POST", "/keys/{id}/rotate", "keys rotate", "", false},
	{"GET", "/keys/{id}/events", "keys events", "", false},

	// Registration.
	{"POST", "/auth/send-otp", "auth register", "", false},
	{"POST", "/auth/verify-otp", "auth register", "", false},

	// The specification itself. main.go registers /v1/openapi.yaml and the CLI
	// fetches it for `api search` and `api describe`, but openapi.yaml does not
	// describe itself -- which is the whole reason the coverage check cannot
	// treat the specification as the list of what exists.
	{"GET", "/openapi.yaml", "api search, api describe", "", true},
}
