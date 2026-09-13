package meta

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryAPIEndpointHasADecision: an entry with no command must say why.
//
// The table is allowed to record "we deliberately do not cover this". What it
// is not allowed to do is leave a blank, because a blank is how an endpoint
// stays uncovered without anyone deciding that it should.
func TestEveryAPIEndpointHasADecision(t *testing.T) {
	if len(Coverage) == 0 {
		t.Fatal("the coverage table is empty; this check would pass on nothing")
	}
	seen := map[string]bool{}
	for _, e := range Coverage {
		key := e.Method + " " + e.Path
		if seen[key] {
			t.Errorf("%s appears twice", key)
		}
		seen[key] = true

		if e.Method == "" || e.Path == "" {
			t.Errorf("%+v has no method or path", e)
		}
		if e.Command == "" && strings.TrimSpace(e.Why) == "" {
			t.Errorf("%s has no command and no reason; decide one or the other", key)
		}
		if e.Command != "" && e.Why != "" {
			t.Errorf("%s has both a command and a reason not to have one", key)
		}
	}
}

// TestEveryCoveredCommandExists: the table names commands, and a name that
// does not resolve is a table that has drifted from the program.
func TestEveryCoveredCommandExists(t *testing.T) {
	bin := buildCLI(t)

	checked := 0
	for _, e := range Coverage {
		if e.Command == "" {
			continue
		}
		// One endpoint may be reached by several commands.
		for _, name := range strings.Split(e.Command, ",") {
			args := append(strings.Fields(strings.TrimSpace(name)), "--help")
			if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
				t.Errorf("%s %s names %q, which does not run: %v\n%s",
					e.Method, e.Path, name, err, out)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no commands were checked")
	}
}

// TestEveryRequestPathIsInTheTable.
//
// The other direction: a path the code calls that nobody wrote down. This is
// what would have caught goals and snippet being added to the API without the
// CLI noticing, had the table existed then.
func TestEveryRequestPathIsInTheTable(t *testing.T) {
	root := repoRoot(t)
	// Paths as they appear in calls: a literal, or a format string whose only
	// verbs are ids.
	call := regexp.MustCompile(`"(/[a-z][a-z0-9/%d{}._-]*)"`)

	// A call may be built with a verb where the table names a literal:
	// settings addresses five endpoints through one fmt.Sprintf. So a call
	// matches when any table entry fits it, not only when one equals it.
	known := make([]string, 0, len(Coverage))
	for _, e := range Coverage {
		known = append(known, normalisePath(e.Path))
	}
	matches := func(call string) bool {
		pat := "^" + strings.ReplaceAll(regexp.QuoteMeta(call), `\*`, `[^/]+`) + "$"
		re, err := regexp.Compile(pat)
		if err != nil {
			return false
		}
		for _, k := range known {
			if re.MatchString(k) {
				return true
			}
		}
		return false
	}

	err := filepath.WalkDir(filepath.Join(root, "internal", "cli"),
		func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range call.FindAllStringSubmatch(string(b), -1) {
				p := normalisePath(m[1])
				// Only paths that look like API calls, not file paths or URLs.
				if !strings.Contains(string(b), "client."+"Get(") &&
					!strings.Contains(string(b), "client.Post(") {
					continue
				}
				if isAPIPath(p) && !matches(p) {
					t.Errorf("%s calls %q, which is not in the coverage table",
						filepath.Base(path), m[1])
				}
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

// normalisePath turns both "%d" and "{id}" into a single placeholder so the
// table and the code can be compared.
func normalisePath(p string) string {
	p = regexp.MustCompile(`\{[a-zA-Z]+\}`).ReplaceAllString(p, "*")
	p = strings.ReplaceAll(p, "%d", "*")
	return strings.ReplaceAll(p, "%s", "*")
}

// isAPIPath filters out the strings that merely look like paths: a config
// file, a device node, a test fixture.
func isAPIPath(p string) bool {
	for _, prefix := range []string{
		"/sites", "/keys", "/query", "/funnels", "/props",
		"/current-visitors", "/subscription", "/auth/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "statable")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/statable")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the CLI failed: %v\n%s", err, out)
	}
	return bin
}
