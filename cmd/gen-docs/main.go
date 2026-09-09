// Command gen-docs writes the shell completions and man pages that ship in a
// release archive. It is part of the module but never linked into the released
// binary, so the cost of generating documentation is not paid by every user.
//
// The man page is written directly as roff rather than through cobra/doc.
// That package renders markdown, and pulling a markdown renderer and its
// parser into an analytics client's dependency tree to produce one page is a
// trade nobody auditing this module would make. The same reasoning kept the
// sparkline hand-written.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/key-arg/statable-cli/internal/cli"
)

func main() {
	out := "."
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	// NewRoot wants real files because the runtime asks them whether they are
	// terminals. Nothing is written to either: only the command tree is read.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		fail(err)
	}
	defer devnull.Close()
	root, _ := cli.NewRoot(devnull, devnull)
	// Completion scripts are generated from the command tree, so a new
	// command or flag is covered the moment it exists.
	root.InitDefaultCompletionCmd()

	if err := writeCompletions(root, filepath.Join(out, "completions")); err != nil {
		fail(err)
	}
	if err := writeManPages(root, filepath.Join(out, "manpages")); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-docs:", err)
	os.Exit(1)
}

func writeCompletions(root *cobra.Command, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, gen := range map[string]func(io.Writer) error{
		"statable.bash": func(w io.Writer) error { return root.GenBashCompletionV2(w, true) },
		"statable.zsh":  root.GenZshCompletion,
		"statable.fish": func(w io.Writer) error { return root.GenFishCompletion(w, true) },
	} {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := gen(f); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// writeManPages writes one page for the root and one per subcommand, which is
// what `man statable-top` has to find.
func writeManPages(root *cobra.Command, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	date := time.Now().UTC().Format("2006-01-02")
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		// Honoured so a rebuild of the same commit produces the same page.
		var secs int64
		if _, err := fmt.Sscanf(s, "%d", &secs); err == nil {
			date = time.Unix(secs, 0).UTC().Format("2006-01-02")
		}
	}

	var walk func(c *cobra.Command) error
	walk = func(c *cobra.Command) error {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return nil
		}
		name := strings.ReplaceAll(c.CommandPath(), " ", "-")
		f, err := os.Create(filepath.Join(dir, name+".1"))
		if err != nil {
			return err
		}
		writeMan(f, c, date)
		if err := f.Close(); err != nil {
			return err
		}
		for _, sub := range c.Commands() {
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

func writeMan(w io.Writer, c *cobra.Command, date string) {
	name := strings.ReplaceAll(c.CommandPath(), " ", "-")

	fmt.Fprintf(w, ".TH %s 1 %q %q %q\n",
		strings.ToUpper(name), date, "statable", "Statable Manual")

	fmt.Fprintf(w, ".SH NAME\n%s \\- %s\n", name, roff(c.Short))

	fmt.Fprintf(w, ".SH SYNOPSIS\n.B %s\n", roff(c.UseLine()))

	body := c.Long
	if body == "" {
		body = c.Short
	}
	fmt.Fprintf(w, ".SH DESCRIPTION\n%s\n", paragraphs(body))

	writeFlags(w, "OPTIONS", c.NonInheritedFlags())
	writeFlags(w, "OPTIONS INHERITED FROM PARENT COMMANDS", c.InheritedFlags())

	if subs := visibleSubcommands(c); len(subs) > 0 {
		fmt.Fprintln(w, ".SH COMMANDS")
		for _, sub := range subs {
			fmt.Fprintf(w, ".TP\n.B %s\n%s\n", roff(sub.Name()), roff(sub.Short))
		}
	}

	if ex := c.Example; ex != "" {
		fmt.Fprintf(w, ".SH EXAMPLES\n%s\n", verbatim(ex))
	}

	fmt.Fprintln(w, ".SH EXIT STATUS")
	for _, row := range [][2]string{
		{"0", "Success."},
		{"1", "Action required. The next_steps field names the fix."},
		{"2", "The operation failed."},
		{"3", "statable check ran and the metric was outside its bounds."},
		{"64", "The command line itself was malformed. Nothing was sent."},
		{"130", "Interrupted."},
	} {
		fmt.Fprintf(w, ".TP\n.B %s\n%s\n", row[0], row[1])
	}

	fmt.Fprintf(w, ".SH SEE ALSO\n%s\n", seeAlso(c))
}

func visibleSubcommands(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		out = append(out, sub)
	}
	return out
}

func writeFlags(w io.Writer, heading string, fs *pflag.FlagSet) {
	if !fs.HasAvailableFlags() {
		return
	}
	fmt.Fprintf(w, ".SH %s\n", heading)
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		fmt.Fprint(w, ".TP\n.B ")
		if f.Shorthand != "" {
			fmt.Fprintf(w, "\\-%s, ", roff(f.Shorthand))
		}
		fmt.Fprintf(w, "\\-\\-%s", roff(f.Name))
		if f.Value.Type() != "bool" {
			fmt.Fprintf(w, " \\fI%s\\fR", roff(f.Value.Type()))
		}
		fmt.Fprintf(w, "\n%s", roff(f.Usage))
		if d := f.DefValue; d != "" && d != "false" && d != "[]" {
			fmt.Fprintf(w, " (default %s)", roff(d))
		}
		fmt.Fprintln(w)
	})
}

func seeAlso(c *cobra.Command) string {
	var refs []string
	if p := c.Parent(); p != nil {
		refs = append(refs, fmt.Sprintf(".BR %s (1)",
			roff(strings.ReplaceAll(p.CommandPath(), " ", "\\-"))))
	}
	for _, sub := range visibleSubcommands(c) {
		refs = append(refs, fmt.Sprintf(".BR %s (1)",
			roff(strings.ReplaceAll(sub.CommandPath(), " ", "\\-"))))
	}
	if len(refs) == 0 {
		return ".BR statable (1)"
	}
	return strings.Join(refs, ",\n")
}

// paragraphs turns blank-line-separated prose into roff paragraphs. A line
// that is indented is treated as preformatted, which is what every example
// block in this program's help text is.
func paragraphs(s string) string {
	var b strings.Builder
	pre := false
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		indented := strings.HasPrefix(line, "  ")
		switch {
		case indented && !pre:
			b.WriteString(".PP\n.RS 4\n.nf\n")
			pre = true
		case !indented && pre:
			b.WriteString(".fi\n.RE\n")
			pre = false
			if strings.TrimSpace(line) == "" {
				continue
			}
			b.WriteString(".PP\n")
		case strings.TrimSpace(line) == "" && !pre:
			b.WriteString(".PP\n")
			continue
		}
		b.WriteString(roff(strings.TrimSpace(line)))
		b.WriteString("\n")
	}
	if pre {
		b.WriteString(".fi\n.RE\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func verbatim(s string) string {
	return ".RS 4\n.nf\n" + roff(strings.TrimSpace(s)) + "\n.fi\n.RE"
}

// roff escapes what would otherwise be read as formatting.
//
// A leading dot or apostrophe starts a request, and a backslash starts an
// escape, so text that begins with one silently disappears from the rendered
// page. The help text of this program contains all three.
func roff(s string) string {
	s = strings.ReplaceAll(s, `\`, `\e`)
	s = strings.ReplaceAll(s, "-", `\-`)
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "'") {
			b.WriteString(`\&`)
		}
		b.WriteString(line)
	}
	return b.String()
}
