package projconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".statable.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAcceptsOnlySiteAndPeriod(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "site: example.com\nperiod: 30d\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site != "example.com" || c.Period != "30d" {
		t.Fatalf("config = %+v", c)
	}
	if len(c.Ignored) != 0 {
		t.Fatalf("nothing should have been ignored: %v", c.Ignored)
	}
}

// TestRefusesSecuritySensitiveKeys is the reason this package exists.
// A repository file is written by whoever can open a pull request; it must
// never be able to redirect the API or supply an identity.
func TestRefusesSecuritySensitiveKeys(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, `
site: example.com
api_url: https://evil.example/api/v1
api-url: https://evil.example/api/v1
base_url: https://evil.example
token: stbl_stolen
api_key: stbl_stolen
insecure_storage: true
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site != "example.com" {
		t.Fatalf("the allowed key should still apply: %+v", c)
	}
	for _, forbidden := range []string{"api_url", "api-url", "base_url", "token", "api_key", "insecure_storage"} {
		found := false
		for _, ig := range c.Ignored {
			if ig == forbidden {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q must be reported as ignored, got %v", forbidden, c.Ignored)
		}
	}
}

// TestNestedValuesCannotSmuggle: an allowed key with a structured value is
// refused rather than coerced.
func TestNestedValuesCannotSmuggle(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "site:\n  id: 1\n  api_url: https://evil.example\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site != "" {
		t.Fatalf("a map must not become a site: %q", c.Site)
	}
	if len(c.Ignored) == 0 {
		t.Fatal("the refusal must be reported")
	}
}

func TestFindWalksUpward(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	want := write(t, root, "site: example.com\n")

	got, ok := Find(deep)
	if !ok {
		t.Fatal("a file in an ancestor directory should be found")
	}
	if got != want {
		t.Fatalf("found %q, want %q", got, want)
	}
}

func TestNearestFileWins(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "site: outer.example\n")
	write(t, sub, "site: inner.example\n")

	c, err := LoadFrom(sub)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site != "inner.example" {
		t.Fatalf("site = %q, want the nearest file to win", c.Site)
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	c, err := LoadFrom(t.TempDir())
	if err != nil {
		t.Fatalf("a missing project file must not be an error: %v", err)
	}
	if c.Site != "" || c.Path != "" {
		t.Fatalf("config = %+v, want zero", c)
	}
}

func TestOversizedFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "site: example.com\n#"+strings.Repeat("x", maxSize+1))
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site != "" {
		t.Fatal("an oversized file must not be parsed")
	}
	if len(c.Ignored) == 0 {
		t.Fatal("refusing the file must be reported")
	}
}

func TestKeysAreCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "SITE: example.com\nPeriod: 7d\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Site != "example.com" || c.Period != "7d" {
		t.Fatalf("config = %+v", c)
	}
}

// TestCollidingKeysAreRefused: Go map iteration is randomised, so a file that
// spells the same key twice would make the CLI query a different site on
// different runs. Measured before the fix: 147/29/24 over 200 loads.
func TestCollidingKeysAreRefused(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "site: lower.example\nSITE: upper.example\nSite: title.example\n")
	for i := 0; i < 50; i++ {
		c, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.Site != "" {
			t.Fatalf("a colliding key must not resolve, got %q", c.Site)
		}
		if len(c.Ignored) == 0 {
			t.Fatal("the collision must be reported")
		}
	}
}

// TestOversizedFileReportsItsPath: the warning used to print an empty filename
// because Path was set after the early return.
func TestOversizedFileReportsItsPath(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "site: example.com\n#"+strings.Repeat("x", maxSize+1))
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != p {
		t.Fatalf("Path = %q, want the file that was refused", c.Path)
	}
	if len(c.Ignored) == 0 {
		t.Fatal("refusing the file must be reported")
	}
}
