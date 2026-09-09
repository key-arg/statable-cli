// Package output owns every byte the CLI writes.
//
// The rule it enforces structurally: when the output format is machine
// readable, the human writer is io.Discard. Not "remember to skip the table",
// not "suppress the spinner in JSON mode" — the stream a stray fmt.Fprintf
// would write to simply is not connected to stdout. Every approach that
// relies on remembering leaks eventually.
//
// Stdout carries the payload and nothing else. Notes, warnings and progress
// go to stderr, so `statable top pages --json | jq` is always valid input.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/key-arg/statable-cli/internal/execctx"
)

// Writer is the only thing a command should print through.
type Writer struct {
	ctx   execctx.Context
	out   io.Writer
	errw  io.Writer
	human io.Writer
}

// New wires a Writer to the given streams. When the format is machine
// readable, human output is discarded rather than suppressed at each site.
func New(ctx execctx.Context, stdout, stderr io.Writer) *Writer {
	human := io.Discard
	if ctx.HumanOutput() {
		human = stdout
	}
	return &Writer{ctx: ctx, out: stdout, errw: stderr, human: human}
}

// Context exposes the resolved invocation context to commands that need to
// branch on it. Commands must not re-derive any of it themselves.
func (w *Writer) Context() execctx.Context { return w.ctx }

// Human is the writer for prose, tables and anything else meant for a person.
// It is io.Discard whenever the format is not human, which is what makes a
// forgotten Println harmless instead of corrupting a JSON payload.
func (w *Writer) Human() io.Writer { return w.human }

// Stdout is the raw payload stream. Only machine payloads and rendered human
// output go here.
func (w *Writer) Stdout() io.Writer { return w.out }

// Stderr carries notes, warnings and progress in every format. Nothing written
// here can corrupt a pipeline.
func (w *Writer) Stderr() io.Writer { return w.errw }

// Printf writes human-facing text. A no-op in machine formats.
func (w *Writer) Printf(format string, a ...any) {
	fmt.Fprintf(w.human, format, a...)
}

// Println writes a human-facing line. A no-op in machine formats.
func (w *Writer) Println(a ...any) {
	fmt.Fprintln(w.human, a...)
}

// sanitizeLine makes one line safe to print.
//
// A note or a warning routinely carries text this program did not write: a
// site name, a funnel name, a path, an error from a parser fed someone else's
// file. Sanitizing at each call site is a rule to remember, and the one that
// was forgotten let a funnel named "Checkout\nFAKE: 999 visitors entered" print
// a second line an operator would read as real, with a live ANSI sequence
// after it that leaked colour into the rest of the session. Doing it here
// makes it a property of the stream instead.
//
// A note is one line by construction, so a newline in it is never intentional.
func sanitizeLine(s string) string { return sanitize(s) }

// Note writes an informational line to stderr. Visible in every format,
// suppressed by --quiet, because a note is for the operator and not for the
// pipeline.
func (w *Writer) Note(format string, a ...any) {
	if w.ctx.Quiet {
		return
	}
	fmt.Fprintln(w.errw, sanitizeLine(fmt.Sprintf(format, a...)))
}

// Warn writes a warning to stderr. Not suppressed by --quiet: a warning the
// operator asked not to see is a warning that will be missed exactly when it
// matters.
func (w *Writer) Warn(format string, a ...any) {
	fmt.Fprintln(w.errw, "warning: "+sanitizeLine(fmt.Sprintf(format, a...)))
}

// JSON writes v to stdout as a single object followed by a newline.
func (w *Writer) JSON(v any) error {
	enc := json.NewEncoder(w.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Raw copies a byte payload straight to stdout, for passing a response
// through untouched rather than re-encoding it.
func (w *Writer) Raw(b []byte) error {
	_, err := w.out.Write(b)
	return err
}

// Table is a set of rows to render. The same value is rendered as an aligned
// table for a person, or as CSV for a machine, so the two modes can never
// drift apart in content.
type Table struct {
	Columns []string
	Rows    [][]string
	// Right lists column indices to right-align in human output. Numbers
	// belong here; text does not.
	Right []int
	// Numeric lists column indices whose cells are numbers. In JSON they are
	// emitted as numbers rather than quoted strings, so a caller can do
	// arithmetic on them without parsing first, and an empty cell becomes
	// null rather than "". CSV and human output are unaffected: both are text
	// by nature.
	Numeric []int
	// Bool lists column indices holding a yes/no. The cell text is "true" or
	// empty. JSON emits a real boolean, CSV writes true or false, and a
	// terminal shows a mark, because a column of the word "false" is noise a
	// reader has to skip and a column of marks is one glance.
	Bool []int
	// HumanOmit lists column indices to leave out of the terminal table while
	// keeping them in JSON and CSV. It is the table counterpart of a Record
	// field's OmitHuman: identifiers and timestamps that a script wants and a
	// person reading four columns of aligned text does not.
	HumanOmit []int
}

// boolMark is what a true cell looks like to a person.
const boolMark = "*"

func indexSet(idx []int) map[int]bool {
	if len(idx) == 0 {
		return nil
	}
	m := make(map[int]bool, len(idx))
	for _, i := range idx {
		m[i] = true
	}
	return m
}

// Num formats an integer for a table cell.
func Num(n int64) string { return strconv.FormatInt(n, 10) }

// Emit renders t in whichever format is active.
func (w *Writer) Emit(t Table) error {
	switch w.ctx.Format {
	case execctx.FormatCSV:
		return w.emitCSV(t)
	case execctx.FormatJSON:
		return w.JSON(tableToObjects(t))
	default:
		return w.emitHuman(t)
	}
}

// row returns a row padded or trimmed to the header's width.
//
// A table's rows and its Columns are built in separate loops, and nothing
// makes them agree. A short row silently dropped a boolean cell in JSON and
// wrote a CSV record with fewer fields than its header, which is a file most
// parsers reject outright; a long one wrote extra CSV fields and lost the same
// values in JSON. The header is the contract, so it decides the width.
func (t Table) row(i int) []string {
	r := t.Rows[i]
	if len(r) == len(t.Columns) {
		return r
	}
	out := make([]string, len(t.Columns))
	copy(out, r)
	return out
}

func tableToObjects(t Table) []map[string]any {
	numeric := indexSet(t.Numeric)
	boolean := indexSet(t.Bool)
	out := make([]map[string]any, 0, len(t.Rows))
	for ri := range t.Rows {
		r := t.row(ri)
		m := make(map[string]any, len(t.Columns))
		for i, c := range t.Columns {
			if boolean[i] {
				m[c] = isTrueCell(r[i])
				continue
			}
			if !numeric[i] {
				m[c] = r[i]
				continue
			}
			if r[i] == "" {
				// A metric the dimension did not compute is absent, not zero.
				m[c] = nil
				continue
			}
			if f, err := strconv.ParseFloat(r[i], 64); err == nil {
				m[c] = f
			} else {
				m[c] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}

// isTrueCell reads a boolean cell. Only "true" is true, so an empty cell and
// an unset one agree instead of depending on which the caller happened to use.
func isTrueCell(s string) bool { return s == "true" }

func (w *Writer) emitCSV(t Table) error {
	boolean := indexSet(t.Bool)
	cw := csv.NewWriter(w.out)
	if err := cw.Write(t.Columns); err != nil {
		return err
	}
	for ri := range t.Rows {
		// Every record is exactly as wide as the header. A CSV whose rows
		// disagree with their header is rejected by most readers, and Go's
		// own csv.Reader is one of them.
		cells := t.row(ri)
		if len(boolean) > 0 {
			// Copied before it is touched: t.row may hand back the caller's
			// own slice, and rewriting a cell in place would change the table
			// the caller still holds.
			cells = append([]string(nil), cells...)
			// A CSV column has to hold the same kind of value in every row,
			// so an empty boolean cell is written as false rather than left
			// blank, which a spreadsheet reads as missing.
			for i := range cells {
				if boolean[i] {
					cells[i] = strconv.FormatBool(isTrueCell(cells[i]))
				}
			}
		}
		if err := cw.Write(cells); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// forHuman drops the machine-only columns and turns booleans into marks,
// remapping the alignment indices so they still point at the columns they
// were given for.
func forHuman(t Table) Table {
	omit := indexSet(t.HumanOmit)
	boolean := indexSet(t.Bool)

	if len(omit) == 0 && len(boolean) == 0 {
		return t
	}

	keep := make([]int, 0, len(t.Columns))
	pos := make(map[int]int, len(t.Columns))
	for i := range t.Columns {
		if omit[i] {
			continue
		}
		pos[i] = len(keep)
		keep = append(keep, i)
	}

	out := Table{Columns: make([]string, 0, len(keep))}
	for _, i := range keep {
		out.Columns = append(out.Columns, t.Columns[i])
	}
	for _, r := range t.Rows {
		row := make([]string, 0, len(keep))
		for _, i := range keep {
			cell := ""
			if i < len(r) {
				cell = r[i]
			}
			if boolean[i] {
				if isTrueCell(cell) {
					cell = boolMark
				} else {
					cell = ""
				}
			}
			row = append(row, cell)
		}
		out.Rows = append(out.Rows, row)
	}
	for _, i := range t.Right {
		if j, ok := pos[i]; ok {
			out.Right = append(out.Right, j)
		}
	}
	return out
}

func (w *Writer) emitHuman(t Table) error {
	t = forHuman(t)
	rows := make([][]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		clean := make([]string, len(r))
		for i, cell := range r {
			clean[i] = sanitize(cell)
		}
		rows = append(rows, clean)
	}

	// A pipe gets no header, so a header must not set a column's width there.
	// It did, which padded every row out to the width of a title nobody could
	// see and left rows ending in spaces whenever the last cell was empty.
	showHeader := w.ctx.StdoutTTY
	widths := make([]int, len(t.Columns))
	if showHeader {
		for i, c := range t.Columns {
			widths[i] = displayWidth(c)
		}
	}
	for _, r := range rows {
		for i, cell := range r {
			if i < len(widths) {
				if n := displayWidth(cell); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}

	right := make(map[int]bool, len(t.Right))
	for _, i := range t.Right {
		right[i] = true
	}

	// A terminal gets a header; a pipe gets bare rows it can cut and awk.
	if showHeader {
		w.writeStyledRow(t.Columns, widths, right, w.dim)
	}
	for _, r := range rows {
		w.writeRow(r, widths, right)
	}
	return nil
}

func (w *Writer) writeRow(cells []string, widths []int, right map[int]bool) {
	w.writeStyledRow(cells, widths, right, nil)
}

func (w *Writer) writeStyledRow(cells []string, widths []int, right map[int]bool, style func(string) string) {
	// Trailing empty cells are dropped so a row never ends in the padding of
	// a column that had nothing in it. A marker column that is empty for all
	// but one row would otherwise leave every other row ending in spaces.
	last := len(cells)
	for last > 0 && cells[last-1] == "" {
		last--
	}
	cells = cells[:last]

	for i, cell := range cells {
		if i > 0 {
			fmt.Fprint(w.human, "  ")
		}
		pad := 0
		if i < len(widths) {
			pad = widths[i] - displayWidth(cell)
		}
		// The last column is never padded: trailing spaces are noise in a
		// terminal and breakage in a diff.
		last := i == len(cells)-1
		shown := cell
		if style != nil {
			shown = style(cell)
		}
		if right[i] {
			fmt.Fprint(w.human, spaces(pad), shown)
		} else if last {
			fmt.Fprint(w.human, shown)
		} else {
			fmt.Fprint(w.human, shown, spaces(pad))
		}
	}
	fmt.Fprintln(w.human)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

// Field is one name/value pair of a single-record result.
type Field struct {
	Name  string
	Value any
	// Human overrides how the value is rendered for a person. Empty means
	// the value is formatted with %v.
	Human string
	// OmitHuman drops the field from human output only, for values that are
	// meaningful to a script but noise on a screen.
	OmitHuman bool
}

// Record is a result with exactly one row: a version, an auth status, a single
// aggregate. It exists so all three formats carry the same content and only
// the encoding differs. Without it, a command that reaches for JSON directly
// silently returns JSON to a caller who asked for CSV.
type Record []Field

// EmitRecord renders r in whichever format is active.
func (w *Writer) EmitRecord(r Record) error {
	switch w.ctx.Format {
	case execctx.FormatJSON:
		m := make(map[string]any, len(r))
		for _, f := range r {
			m[f.Name] = f.Value
		}
		return w.JSON(m)

	case execctx.FormatCSV:
		cols := make([]string, 0, len(r))
		row := make([]string, 0, len(r))
		for _, f := range r {
			cols = append(cols, f.Name)
			row = append(row, flatten(f.Value))
		}
		return w.emitCSV(Table{Columns: cols, Rows: [][]string{row}})

	default:
		// A single visible value needs no label: "statable dev" reads better
		// than "version: statable dev". Fields hidden from human output do
		// not count towards that, or a record with one visible field and one
		// machine-only field would still be labelled.
		visible := make([]Field, 0, len(r))
		for _, f := range r {
			if !f.OmitHuman {
				visible = append(visible, f)
			}
		}
		if len(visible) == 1 {
			v := visible[0].Human
			if v == "" {
				v = flatten(visible[0].Value)
			}
			fmt.Fprintln(w.human, sanitize(v))
			return nil
		}
		width := 0
		for _, f := range r {
			if f.OmitHuman {
				continue
			}
			// The rendered label carries a colon, so the width has to
			// account for it or the longest name overflows its own column.
			if n := displayWidth(f.Name) + 1; n > width {
				width = n
			}
		}
		for _, f := range r {
			if f.OmitHuman {
				continue
			}
			v := f.Human
			if v == "" {
				v = flatten(f.Value)
			}
			fmt.Fprintf(w.human, "%-*s  %s\n", width, f.Name+":", sanitize(v))
		}
		return nil
	}
}

// displayWidth returns how many terminal columns a string occupies.
//
// Counting runes is right for Latin and wrong for everything else: a CJK
// ideograph and most emoji occupy two columns, so a table of page paths in
// Chinese or with an emoji in a title had every following column shifted.
// Combining marks occupy none.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	// Combining marks and other zero-width characters.
	case r >= 0x0300 && r <= 0x036f,
		r >= 0x200b && r <= 0x200f,
		r == 0xfeff,
		r >= 0xfe00 && r <= 0xfe0f:
		return 0
	// The East Asian Wide and Fullwidth blocks, and the emoji planes.
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x2e80 && r <= 0x303e,
		r >= 0x3041 && r <= 0x33ff,
		r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff,
		r >= 0xa000 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe6f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// sanitize makes a value safe to draw inside a table cell.
//
// Server data — a page path, a referrer, a field description — may contain a
// newline or a tab; printed raw it splits one row across two lines and every
// column after it stops lining up. Machine formats keep the value intact,
// because CSV quotes it and JSON escapes it.
//
// Whitespace control characters become a space rather than a replacement
// character: a wrapped sentence should read as a sentence. Anything else
// becomes U+FFFD, because a value carrying a control byte is worth seeing.
func sanitize(s string) string {
	if strings.IndexFunc(s, unsafeForCell) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r',
			r == 0x85, r == 0x2028, r == 0x2029:
			return ' '
		case unsafeForCell(r):
			return '\uFFFD'
		}
		return r
	}, s)
}

// unsafeForCell reports whether a rune must not be written into a table cell.
//
// The C0 block and DEL are the obvious ones. The rest are less obvious and
// were missed at first: U+0085, U+2028 and U+2029 are line breaks a terminal
// honours; the C1 block includes U+009B, the eight-bit form of the escape
// sequence introducer; and the bidirectional overrides can reorder a line so
// it reads as something other than what it says.
func unsafeForCell(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x2028 || r == 0x2029:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// Sanitize makes a value safe to write into a terminal line. Commands that
// print outside a table still have to pass server-supplied text through it.
func Sanitize(s string) string { return sanitize(s) }

// flatten renders a value for a single text cell.
//
// Numbers are the reason this is not just fmt.Sprint. %v on a float64 uses %g,
// so a site with a million pageviews produced "9.87654321e+09" in CSV while
// the JSON of the same command carried 9876543210. A spreadsheet and awk both
// choke on the first. Whole numbers are printed as integers and fractions in
// full decimal, matching what the JSON encoder would have written.
//
// A nil is an absent value, not the word "<nil>": query.md says a rate may be
// null when it is undefined for the window, and an empty cell is how CSV says
// that.
func flatten(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case []string:
		return strings.Join(n, "; ")
	case float64:
		return formatFloat(n)
	case float32:
		return formatFloat(float64(n))
	}
	return fmt.Sprint(v)
}

func formatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ANSI styling. The set is deliberately tiny: a dim header and nothing else.
// Colour here exists so --no-color and NO_COLOR are honoured by something
// rather than being flags that describe an intention the code never acts on.
const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// dim styles text for a person, and returns it untouched when colour is off.
func (w *Writer) dim(s string) string {
	if !w.ctx.UseColor() {
		return s
	}
	return ansiDim + s + ansiReset
}
