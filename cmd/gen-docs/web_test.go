package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/key-arg/statable-cli/internal/cli"
)

// The two shapes hooks/check_cli_commands.py in statable.com-docs looks for.
// They are copied rather than shared because the hook lives in another
// repository; if either side changes, this test is where the mismatch shows.
var (
	hookHeading  = regexp.MustCompile("(?m)^#{2,4} `(statable[^`]*)`\\s*$")
	hookIndexRow = regexp.MustCompile("(?m)^\\| \\[`(statable[^`]*)`\\]\\(#")
)

func testRoot(t *testing.T) *cobra.Command {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	root, _ := cli.NewRoot(devnull, devnull)
	return root
}

func generate(t *testing.T) (string, manifest) {
	t.Helper()
	root := testRoot(t)
	var page, man bytes.Buffer
	writeWeb(&page, root, "v9.9.9")
	if err := writeManifest(&man, root, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(man.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return page.String(), m
}

func unique(matches [][]string) []string {
	seen := map[string]bool{}
	for _, m := range matches {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestThePagePassesTheDocsBuild runs the docs build's own checks against a
// freshly generated page, so a change that would fail that build fails here
// first, where the generator can be fixed.
func TestThePagePassesTheDocsBuild(t *testing.T) {
	page, m := generate(t)
	want := strings.Join(m.Commands, "\n")
	// The index table and every subcommand table use the same row shape, so
	// the set of rows is what the hook compares, not their count.
	for name, got := range map[string][]string{
		"sections":   unique(hookHeading.FindAllStringSubmatch(page, -1)),
		"index rows": unique(hookIndexRow.FindAllStringSubmatch(page, -1)),
	} {
		if strings.Join(got, "\n") != want {
			t.Errorf("%s disagree with the manifest:\n got %v\nwant %v", name, got, m.Commands)
		}
	}
	if !strings.Contains(page, "**v9.9.9**") {
		t.Error("the page does not name the version it was generated for, and the docs build requires it")
	}
	if m.Count != len(m.Commands) {
		t.Errorf("manifest count %d, but it lists %d commands", m.Count, len(m.Commands))
	}
}

// TestTheManifestIsTheWholeTree guards the other direction: a page and
// manifest that agree with each other by both leaving a command out would pass
// the docs build.
func TestTheManifestIsTheWholeTree(t *testing.T) {
	_, m := generate(t)
	var want []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		want = append(want, c.CommandPath())
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(testRoot(t))
	sort.Strings(want)
	if strings.Join(m.Commands, "\n") != strings.Join(want, "\n") {
		t.Errorf("manifest is not the command tree:\n got %v\nwant %v", m.Commands, want)
	}
	for _, c := range m.Commands {
		if c == "statable help" || c == "statable completion" {
			t.Errorf("%q is cobra's, not ours, and has no place in the reference", c)
		}
	}
}

// sections splits the page into command name -> section body.
func sections(page string) map[string]string {
	out := map[string]string{}
	idx := hookHeading.FindAllStringSubmatchIndex(page, -1)
	for i, m := range idx {
		end := len(page)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out[page[m[2]:m[3]]] = page[m[1]:end]
	}
	return out
}

// TestEveryFlagIsUnderTheCommandThatDefinesIt. A flag a command defines is in
// its own table; a flag it inherits is not repeated there, because the page
// would otherwise list the eight global flags forty-five times.
func TestEveryFlagIsUnderTheCommandThatDefinesIt(t *testing.T) {
	page, _ := generate(t)
	secs := sections(page)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		body, ok := secs[c.CommandPath()]
		if !ok {
			t.Fatalf("no section for %s", c.CommandPath())
		}
		table := ""
		if i := strings.Index(body, "**Flags**"); i >= 0 {
			table = body[i:]
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			if !flagCell(f.Name).MatchString(table) {
				t.Errorf("%s: --%s is missing from its flags table", c.CommandPath(), f.Name)
			}
		})
		c.InheritedFlags().VisitAll(func(f *pflag.Flag) {
			// Matched whole: --key is a prefix of --key-name, and a
			// substring check reported the one as a repeat of the other.
			if flagCell(f.Name).MatchString(table) {
				t.Errorf("%s: inherited --%s is repeated in its own table", c.CommandPath(), f.Name)
			}
		})
		for _, sub := range visibleSubcommands(c) {
			walk(sub)
		}
	}
	walk(testRoot(t))
}

// flagCell matches the flag's own cell, `--name` or `--name type`.
func flagCell(name string) *regexp.Regexp {
	return regexp.MustCompile("`--" + regexp.QuoteMeta(name) + "( [A-Za-z0-9]+)?`")
}

func TestIndentedHelpBecomesAShellBlock(t *testing.T) {
	got := markdownBody("Read it first:\n\n  statable settings hostnames\n  statable settings set hostnames --allow a\n\nThen change it.")
	want := "Read it first:\n\n```bash\nstatable settings hostnames\nstatable settings set hostnames --allow a\n```\n\nThen change it."
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A pipe in help text would end a table cell early and shift every column
// after it, silently.
func TestAPipeDoesNotBreakATableRow(t *testing.T) {
	f := &pflag.Flag{Name: "format", Usage: "human | json", Value: newString(""), DefValue: ""}
	if got := flagMeaning(f); got != `human \| json` {
		t.Errorf("got %q", got)
	}
}

func TestZeroDefaultsAreNotShown(t *testing.T) {
	for def, want := range map[string]string{
		"":      "x",
		"false": "x",
		"0":     "x",
		"[]":    "x",
		"7d":    "x (default `7d`)",
		"[a,b]": "x (default `[a,b]`)",
	} {
		f := &pflag.Flag{Name: "n", Usage: "x", Value: newString(def), DefValue: def}
		if got := flagMeaning(f); got != want {
			t.Errorf("default %q: got %q, want %q", def, got, want)
		}
	}
}

func newString(s string) pflag.Value {
	fs := pflag.NewFlagSet("", pflag.ContinueOnError)
	return fs.VarPF(&stringValue{s}, "v", "", "").Value
}

type stringValue struct{ s string }

func (v *stringValue) String() string     { return v.s }
func (v *stringValue) Set(s string) error { v.s = s; return nil }
func (v *stringValue) Type() string       { return "string" }
