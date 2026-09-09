// Package meta holds checks about the repository itself rather than about any
// one package's behaviour.
package meta

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryFuzzTargetIsRunInCI.
//
// A fuzz target that nobody runs is a corpus that never grows and a parser
// that is never exercised on anything but its seeds. The targets are listed by
// hand in the workflow — deliberately, because a loop over them relied on
// shell word splitting — and a hand-written list is exactly the kind that goes
// out of date the day someone adds a seventh target.
func TestEveryFuzzTargetIsRunInCI(t *testing.T) {
	root := repoRoot(t)

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("the CI workflow is missing: %v", err)
	}
	ci := string(workflow)

	found := 0
	fuzzFunc := regexp.MustCompile(`(?m)^func (Fuzz\w+)\(`)
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pkgDir := filepath.Dir(path)
		rel, err := filepath.Rel(root, pkgDir)
		if err != nil {
			return err
		}
		pkg := "./" + filepath.ToSlash(rel)

		for _, m := range fuzzFunc.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			found++
			if !strings.Contains(ci, "-fuzz="+name) {
				t.Errorf("%s in %s is never run: add it to .github/workflows/ci.yml", name, pkg)
				continue
			}
			// Named is not enough — it has to be run against its own package,
			// or the step fails on the day it finally matters.
			if !regexp.MustCompile(regexp.QuoteMeta(pkg) + `\s+-run=\S+\s+-fuzz=` + regexp.QuoteMeta(name)).MatchString(ci) {
				t.Errorf("%s is run in CI but not against %s", name, pkg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no fuzz targets were found at all; this check would pass on an empty repository")
	}
}

// repoRoot walks up to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}
