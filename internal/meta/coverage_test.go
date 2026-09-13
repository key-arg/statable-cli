package meta

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	if problems := Validate(Coverage); len(problems) > 0 {
		for _, p := range problems {
			t.Error(p)
		}
	}
}

// TestValidateCatchesABrokenTable: the check above can only ever see a correct
// table, so on its own it proves nothing. These are the shapes it must reject.
func TestValidateCatchesABrokenTable(t *testing.T) {
	cases := []struct {
		name  string
		table []APIEndpoint
		want  string
	}{
		{"no decision", []APIEndpoint{{Method: "GET", Path: "/x"}}, "no command and no reason"},
		{"both", []APIEndpoint{{Method: "GET", Path: "/x", Command: "c", Why: "w"}}, "both a command"},
		{"duplicate", []APIEndpoint{
			{Method: "GET", Path: "/x", Command: "c"},
			{Method: "GET", Path: "/x", Command: "c"},
		}, "appears twice"},
		{"no path", []APIEndpoint{{Method: "GET", Command: "c"}}, "no method or path"},
		{"empty", nil, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := Validate(tc.table)
			if len(problems) == 0 {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("the complaint does not mention %q: %v", tc.want, problems)
			}
		})
	}

	// And a correct table produces no complaints, or the check is just noise.
	if problems := Validate([]APIEndpoint{
		{Method: "GET", Path: "/a", Command: "a"},
		{Method: "POST", Path: "/a", Why: "writes are out of scope"},
	}); len(problems) > 0 {
		t.Fatalf("a correct table was rejected: %v", problems)
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
			name = strings.TrimSpace(name)
			args := append(strings.Fields(name), "--help")
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
			// The guard used to be per FILE: a file with no client.Get( or
			// client.Post( was skipped entirely, which exempted every path in
			// settings_write.go because its calls go through a helper. isAPIPath
			// already does the filtering, so the file-level test only ever
			// created a blind spot.
			for _, m := range call.FindAllStringSubmatch(string(b), -1) {
				p := normalisePath(m[1])
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
	// Windows will not execute a file without the extension, and go build
	// does not add one for you when -o names the path. The first version of
	// this failed every row of the table on Windows for that reason alone.
	name := "statable"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/statable")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the CLI failed: %v\n%s", err, out)
	}
	return bin
}
