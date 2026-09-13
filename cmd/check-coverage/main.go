// Command check-coverage compares the coverage table against what the API
// really exposes.
//
// It replaces a shell script that parsed openapi.yaml with a hand-written
// regular expression. That script was wrong in four ways at once: a comment or
// a document separator inside the paths block ended the parse silently, a
// quoted or $ref'd path vanished, `comm ... || true` turned any failure into a
// clean pass, and an HTML login page in place of the specification produced
// zero endpoints and therefore agreement. This uses the YAML parser already in
// go.mod and refuses to agree with nothing.
//
// The specification is not the whole truth either. GET /v1/openapi.yaml is
// registered in the router and absent from the document that router serves, so
// --routes takes a Go source file and reads the registrations directly. The
// specification says what is promised; the router says what exists.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/key-arg/statable-cli/internal/meta"
)

// minimumEndpoints is a floor, not a guess. The API has had more than thirty
// operations for as long as this tool has existed; a specification that parses
// to a handful is a specification that did not arrive.
const minimumEndpoints = 20

func main() {
	spec := flag.String("spec", "https://statable.com/api/v1/openapi.yaml",
		"the specification to compare against: a URL or a file")
	routes := flag.String("routes", "",
		"optional Go source registering the routes, compared as well")
	flag.Parse()

	live, err := loadSpec(*spec)
	if err != nil {
		fail("reading the specification: %v", err)
	}
	if err := checkSpecSane(live); err != nil {
		fail("%v", err)
	}

	table := map[string]bool{}
	promised := map[string]bool{}
	for _, e := range meta.Coverage {
		key := e.Method + " " + e.Path
		table[key] = true
		if !e.NotInSpec {
			promised[key] = true
		}
	}

	var problems []string

	for _, k := range sortedKeys(live) {
		if !table[k] {
			problems = append(problems, fmt.Sprintf(
				"  %s is served but missing from internal/meta/coverage.go", k))
		}
	}
	for _, k := range sortedKeys(promised) {
		if !live[k] {
			problems = append(problems, fmt.Sprintf(
				"  %s is in the coverage table but the specification no longer describes it", k))
		}
	}

	checkedRoutes := 0
	if *routes != "" {
		router, rerr := loadRoutes(*routes)
		if rerr != nil {
			fail("reading the routes: %v", rerr)
		}
		checkedRoutes = len(router)
		for _, k := range sortedKeys(router) {
			if !table[k] {
				problems = append(problems, fmt.Sprintf(
					"  %s is registered in the router and missing from the coverage table", k))
			}
		}
	}

	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "The coverage table disagrees with the API:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintln(os.Stderr,
			"\nAdd each to internal/meta/coverage.go with a command, or with a reason there is none.")
		os.Exit(1)
	}

	fmt.Printf("Coverage table agrees with the specification: %d endpoints", len(live))
	if checkedRoutes > 0 {
		fmt.Printf(", and with %d routes in the router", checkedRoutes)
	}
	fmt.Println(".")
}

// checkSpecSane refuses to compare against a document that plainly is not the
// specification. Zero endpoints agreed with any table at all, which is the
// most dangerous answer this tool could give.
func checkSpecSane(live map[string]bool) error {
	if len(live) < minimumEndpoints {
		return fmt.Errorf(
			"the specification parsed to %d endpoints, which is fewer than the %d this API has had for years.\n"+
				"That usually means the document did not arrive: a login page, an error body, or an empty paths block",
			len(live), minimumEndpoints)
	}
	return nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// specDoc is the shape needed: the paths map, with each path's methods.
type specDoc struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

var methods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

func loadSpec(src string) (map[string]bool, error) {
	var body []byte
	var err error
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		body, err = fetch(src)
	} else {
		body, err = os.ReadFile(src)
	}
	if err != nil {
		return nil, err
	}

	var doc specDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the specification is not YAML this understands: %w", err)
	}
	out := map[string]bool{}
	for path, ops := range doc.Paths {
		for m := range ops {
			if methods[strings.ToLower(m)] {
				out[strings.ToUpper(m)+" "+path] = true
			}
		}
	}
	return out, nil
}

func fetch(url string) ([]byte, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// routeLine matches a mux registration. Two styles appear in the same file:
// on the root router with an explicit /v1 prefix, and on a subrouter already
// mounted at /v1 with a relative path. The receiver name distinguishes them,
// and the first version of this regexp recognised only the first style -- so
// it read one route out of thirty-six and reported agreement.
// Each registration is one line, and the handler between the path and
// .Methods often carries its own parentheses -- app.apiWriteGuard(h) -- which a
// single expression cannot span without recursion. So the line is found first
// and the two parts are read out of it separately. Matching greedily across
// the wrapper read eight of thirty-six registrations and called that agreement.
var routeLine = regexp.MustCompile(`(\w+)\.HandleFunc\(\s*"([^"]*)"`)

var methodsCall = regexp.MustCompile(`\.Methods\(([^)]*)\)`)

// v1Subrouters are the receivers already mounted under /v1. Anything else has
// to carry the prefix in its path to count.
var v1Subrouters = map[string]bool{"apiV1": true}

var methodArg = regexp.MustCompile(`"([A-Za-z]+)"|http\.Method([A-Za-z]+)`)

// pathVar turns mux's {id:[0-9]+} into the {id} the specification writes.
var pathVar = regexp.MustCompile(`\{([a-zA-Z]+):[^}]*\}`)

func loadRoutes(file string) (map[string]bool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		m := routeLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		verbs := methodsCall.FindStringSubmatch(line)
		if verbs == nil {
			// A registration with no .Methods answers every verb; this tool
			// has nothing useful to say about that, and the v1 surface does
			// not use it.
			continue
		}
		recv, raw := m[1], m[2]
		var path string
		switch {
		case v1Subrouters[recv]:
			path = raw
		case strings.HasPrefix(raw, "/v1/"):
			path = strings.TrimPrefix(raw, "/v1")
		default:
			// Not part of the v1 surface this CLI speaks to.
			continue
		}
		path = pathVar.ReplaceAllString(path, "{$1}")
		for _, mm := range methodArg.FindAllStringSubmatch(verbs[1], -1) {
			verb := mm[1]
			if verb == "" {
				verb = mm[2]
			}
			if methods[strings.ToLower(verb)] {
				out[strings.ToUpper(verb)+" "+path] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s registered no /v1 routes this could read", file)
	}
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
