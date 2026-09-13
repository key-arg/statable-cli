package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSpecParsingSurvivesWhatBrokeTheScript.
//
// Each of these is a shape the hand-written parser this replaced got wrong. A
// comment at column zero ended its parse silently, taking every later path with
// it; a quoted key vanished; a path containing a colon confused it.
func TestSpecParsingSurvivesWhatBrokeTheScript(t *testing.T) {
	const spec = `
openapi: 3.1.0
info:
  title: t
paths:
# a comment at column zero
  /sites:
    get:
      summary: list
    post:
      summary: create
  "/quoted/path":
    get:
      summary: quoted keys are legal YAML
  /weird:path:
    delete:
      summary: a colon in a path
  /ignored:
    parameters: []
`
	dir := t.TempDir()
	f := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(f, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSpec(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"GET /sites", "POST /sites", "GET /quoted/path", "DELETE /weird:path",
	} {
		if !got[want] {
			t.Errorf("%q was not parsed out of the specification", want)
		}
	}
	// `parameters` is not a method and must not be counted as one.
	if len(got) != 4 {
		t.Errorf("parsed %d operations, want 4: %v", len(got), sortedKeys(got))
	}
}

// TestSpecMustNotBeHTML: a login page or an error body parsed to nothing, and
// nothing agreed with everything.
func TestSpecMustNotBeHTML(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "login.html")
	if err := os.WriteFile(f, []byte("<html><body>Sign in</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSpec(f); err == nil {
		t.Fatal("an HTML page was accepted as a specification")
	}
}

// TestRouteParsingReadsBothRegistrationStyles.
//
// The same file registers routes two ways: on the root router with an explicit
// /v1 prefix, and on a subrouter already mounted there. The first version of
// this parser understood only the former and read one route out of thirty-six,
// then reported agreement.
func TestRouteParsingReadsBothRegistrationStyles(t *testing.T) {
	const src = `
package main

func routes() {
	r.HandleFunc("/v1/openapi.yaml", openAPISpecHandler).Methods("GET")
	r.HandleFunc("/v1/auth/send-otp", app.apiBootstrapGuard(app.apiAuthSendOTPHandler)).Methods("POST")
	apiV1.HandleFunc("/props", app.apiListPropsHandler).Methods("GET")
	apiV1.HandleFunc("/sites/{id:[0-9]+}/goals", app.apiWriteGuard(app.apiListGoalsHandler)).Methods("GET")
	apiV1.HandleFunc("/sites/{id:[0-9]+}", app.apiWriteGuard(app.apiPatchSiteHandler)).Methods("PATCH")
	apiV1.HandleFunc("/keys/{id:[0-9]+}", app.apiWriteGuard(app.apiDeleteKeyHandler)).Methods(http.MethodDelete)
	other.HandleFunc("/not-v1/thing", h).Methods("GET")
	apiV1.HandleFunc("/no-methods", h)
}
`
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadRoutes(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"GET /openapi.yaml",     // root router, prefix stripped
		"POST /auth/send-otp",   // wrapped handler with its own parentheses
		"GET /props",            // subrouter, relative path
		"GET /sites/{id}/goals", // mux pattern reduced to the spec's form
		"PATCH /sites/{id}",     // a verb that is not GET or POST
		"DELETE /keys/{id}",     // http.MethodDelete rather than a literal
	} {
		if !got[want] {
			t.Errorf("%q was not read out of the router: got %v", want, sortedKeys(got))
		}
	}
	// Routes outside v1, and registrations with no .Methods, are not ours.
	for _, no := range []string{"GET /not-v1/thing", "GET /no-methods"} {
		if got[no] {
			t.Errorf("%q should not have been counted", no)
		}
	}
	if len(got) != 6 {
		t.Errorf("read %d routes, want 6: %v", len(got), sortedKeys(got))
	}
}

// TestEmptyRouterIsAnError: a file this cannot read must say so rather than
// quietly agreeing with a table it never compared anything to.
func TestEmptyRouterIsAnError(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	if err := os.WriteFile(f, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRoutes(f); err == nil {
		t.Fatal("a file with no routes was accepted")
	}
}

// TestSpecSanityFloor: zero endpoints agreed with every table, which is the
// most dangerous answer this tool could give. The floor lives in a function so
// a test can reach it.
func TestSpecSanityFloor(t *testing.T) {
	if err := checkSpecSane(map[string]bool{}); err == nil {
		t.Error("an empty specification was accepted")
	}
	thin := map[string]bool{"GET /a": true, "GET /b": true}
	if err := checkSpecSane(thin); err == nil {
		t.Errorf("a specification of %d endpoints was accepted", len(thin))
	}

	full := map[string]bool{}
	for i := 0; i < minimumEndpoints; i++ {
		full[string(rune('a'+i))] = true
	}
	if err := checkSpecSane(full); err != nil {
		t.Errorf("a full specification was rejected: %v", err)
	}
}
