package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The web reference is the page statable.com/docs/cli/commands/ publishes.
// It is generated for the same reason the man pages are: a reference typed
// by hand drifts from the binary within one release, and still looks
// authoritative while it does.
//
// The manifest written beside it is what the docs build checks the page
// against, so a section tidied away by hand fails the build there. It cannot
// notice a new release on its own; that is what regenerating from the tag is
// for.

// webCommands is the tree in the order the page lists it: depth-first, with
// cobra's alphabetical ordering of siblings.
func webCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		out = append(out, c)
		for _, sub := range visibleSubcommands(c) {
			walk(sub)
		}
	}
	walk(root)
	return out
}

// writeWeb writes the MkDocs page. The heading and index-row shapes are a
// contract with hooks/check_cli_commands.py in statable.com-docs: change them
// here and that build stops recognising the page.
func writeWeb(w io.Writer, root *cobra.Command, version string) {
	cmds := webCommands(root)

	fmt.Fprint(w, "---\n"+
		"html_title: 'Statable CLI commands: the full reference'\n"+
		"description: 'Every statable command with its flags, generated from the binary itself, so the page cannot drift from what the tool accepts.'\n"+
		"---\n\n"+
		"# Commands\n\n")
	fmt.Fprintf(w, "Every command `statable` accepts, %d of them, with the flags each one takes. "+
		"The flags that work everywhere are under [`statable`](#statable).\n\n", len(cmds)-1)
	fmt.Fprintf(w, "This page is generated from the command tree of the binary, so it says what the tool "+
		"actually accepts rather than what somebody remembered to write down. It describes **%s**; "+
		"`statable version` tells you which one you have.\n\n", version)
	fmt.Fprint(w, "Run any of these with `--help` for the same text in your terminal, or read "+
		"`man statable-top` and friends, which ship in the release archive.\n\n")

	fmt.Fprint(w, "## Every command\n\n")
	writeCommandTable(w, "Command", cmds)

	for _, c := range cmds {
		writeWebSection(w, root, c)
	}
}

func writeCommandTable(w io.Writer, heading string, cmds []*cobra.Command) {
	fmt.Fprintf(w, "| %s | What it does |\n|---|---|\n", heading)
	for _, c := range cmds {
		fmt.Fprintf(w, "| [`%s`](#%s) | %s |\n", c.CommandPath(), anchor(c), cell(c.Short))
	}
	fmt.Fprintln(w)
}

func writeWebSection(w io.Writer, root, c *cobra.Command) {
	// The root is ##, its children ###, and everything deeper ####: MkDocs
	// renders a fifth level too small to find, and the table of contents
	// stays three levels deep.
	depth := strings.Count(c.CommandPath(), " ")
	level := strings.Repeat("#", min(depth+2, 4))
	fmt.Fprintf(w, "%s `%s`\n\n", level, c.CommandPath())
	fmt.Fprintf(w, "%s\n\n", sentence(c.Short))
	fmt.Fprintf(w, "```\n%s\n```\n\n", c.UseLine())

	if long := strings.TrimSpace(c.Long); long != "" {
		fmt.Fprintf(w, "%s\n\n", markdownBody(long))
	}
	if ex := strings.TrimSpace(c.Example); ex != "" {
		fmt.Fprintf(w, "Examples:\n```bash\n%s\n```\n\n", dedent(c.Example))
	}
	if subs := visibleSubcommands(c); len(subs) > 0 {
		writeCommandTable(w, "Subcommand", subs)
	}
	// Only the flags this command defines. What it inherits is either global,
	// and listed once under the root, or belongs to a parent that lists it.
	if fs := c.LocalFlags(); hasVisibleFlags(fs) {
		fmt.Fprint(w, "**Flags**\n\n| Flag | Meaning |\n|---|---|\n")
		fs.VisitAll(func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			fmt.Fprintf(w, "| %s | %s |\n", flagName(f), flagMeaning(f))
		})
		fmt.Fprintln(w)
	}
	if c != root {
		fmt.Fprint(w, "Plus the [global flags](#statable).\n\n")
	}
}

func hasVisibleFlags(fs *pflag.FlagSet) bool {
	found := false
	fs.VisitAll(func(f *pflag.Flag) {
		if !f.Hidden && f.Name != "help" {
			found = true
		}
	})
	return found
}

func flagName(f *pflag.Flag) string {
	name := "--" + f.Name
	if t := f.Value.Type(); t != "bool" {
		name += " " + t
	}
	if f.Shorthand != "" {
		return fmt.Sprintf("`-%s`, `%s`", f.Shorthand, name)
	}
	return "`" + name + "`"
}

func flagMeaning(f *pflag.Flag) string {
	s := cell(f.Usage)
	switch d := f.DefValue; d {
	case "", "false", "[]", "0":
	default:
		s += fmt.Sprintf(" (default `%s`)", d)
	}
	return s
}

// markdownBody keeps the help text as written and fences the indented runs,
// which in this program's help are always shell examples.
func markdownBody(s string) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "  ") {
			b.WriteString(lines[i])
			b.WriteString("\n")
			continue
		}
		b.WriteString("```bash\n")
		for ; i < len(lines) && strings.HasPrefix(lines[i], "  "); i++ {
			b.WriteString(strings.TrimSpace(lines[i]))
			b.WriteString("\n")
		}
		b.WriteString("```\n")
		i--
	}
	return strings.TrimRight(b.String(), "\n")
}

func dedent(s string) string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		out = append(out, strings.TrimSpace(line))
	}
	return strings.Join(out, "\n")
}

func anchor(c *cobra.Command) string {
	return strings.ReplaceAll(c.CommandPath(), " ", "-")
}

// cell keeps a table row one row: a pipe would end the cell early.
func cell(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "|", `\|`)
}

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s[len(s)-1:], ".!?") {
		return s
	}
	return s + "."
}

type manifest struct {
	Version  string   `json:"version"`
	Count    int      `json:"count"`
	Commands []string `json:"commands"`
}

func writeManifest(w io.Writer, root *cobra.Command, version string) error {
	m := manifest{Version: version}
	for _, c := range webCommands(root) {
		m.Commands = append(m.Commands, c.CommandPath())
	}
	sort.Strings(m.Commands)
	m.Count = len(m.Commands)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}
