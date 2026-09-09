package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/key-arg/statable-cli/internal/execctx"
)

func newTest(f execctx.Format, tty bool) (*Writer, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	// Colour off by default so assertions read the text, not the escapes.
	// TestColourIsAppliedAndSuppressed covers the styling itself.
	ctx := execctx.Context{Format: f, StdoutTTY: tty, NoColor: true}
	return New(ctx, &out, &errb), &out, &errb
}

// stripANSI removes styling so a test can assert on content.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestHumanWriterIsDiscardedInMachineFormats is the guarantee the whole
// package exists for. A command that forgets itself and prints prose must not
// be able to corrupt a JSON or CSV payload.
func TestHumanWriterIsDiscardedInMachineFormats(t *testing.T) {
	for _, f := range []execctx.Format{execctx.FormatJSON, execctx.FormatCSV} {
		t.Run(string(f), func(t *testing.T) {
			w, out, _ := newTest(f, true)

			w.Printf("visitors: %d\n", 42)
			w.Println("this line is for a person")
			if _, err := w.Human().Write([]byte("and so is this")); err != nil {
				t.Fatalf("writing to Human() must never fail: %v", err)
			}

			if out.Len() != 0 {
				t.Fatalf("human output leaked into stdout: %q", out.String())
			}
		})
	}
}

func TestHumanWriterReachesStdoutInHumanFormat(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	w.Println("hello")
	if got := out.String(); got != "hello\n" {
		t.Fatalf("stdout = %q, want %q", got, "hello\n")
	}
}

// TestNotesNeverTouchStdout keeps pipelines valid: anything advisory belongs
// on stderr in every format.
func TestNotesNeverTouchStdout(t *testing.T) {
	for _, f := range []execctx.Format{execctx.FormatHuman, execctx.FormatJSON, execctx.FormatCSV} {
		t.Run(string(f), func(t *testing.T) {
			w, out, errb := newTest(f, true)
			w.Note("using site %s", "example.com")
			w.Warn("rate limit is nearly spent")

			if out.Len() != 0 {
				t.Fatalf("notes leaked into stdout: %q", out.String())
			}
			if !strings.Contains(errb.String(), "example.com") {
				t.Fatalf("note missing from stderr: %q", errb.String())
			}
			if !strings.Contains(errb.String(), "warning: rate limit") {
				t.Fatalf("warning missing from stderr: %q", errb.String())
			}
		})
	}
}

// TestQuietSuppressesNotesNotWarnings: a warning the operator asked not to see
// is one that will be missed exactly when it matters.
func TestQuietSuppressesNotesNotWarnings(t *testing.T) {
	var out, errb bytes.Buffer
	ctx := execctx.Context{Format: execctx.FormatHuman, StdoutTTY: true, Quiet: true}
	w := New(ctx, &out, &errb)

	w.Note("this should vanish")
	w.Warn("this must not")

	s := errb.String()
	if strings.Contains(s, "vanish") {
		t.Fatalf("--quiet must suppress notes, got %q", s)
	}
	if !strings.Contains(s, "must not") {
		t.Fatalf("--quiet must not suppress warnings, got %q", s)
	}
}

var sample = Table{
	Columns: []string{"page", "visitors"},
	Rows: [][]string{
		{"/", "1204"},
		{"/pricing", "87"},
	},
	Right: []int{1},
}

func TestEmitCSV(t *testing.T) {
	w, out, _ := newTest(execctx.FormatCSV, false)
	if err := w.Emit(sample); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("emitted CSV does not parse: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want header plus two rows", len(recs))
	}
	if recs[0][0] != "page" || recs[2][1] != "87" {
		t.Fatalf("unexpected records: %v", recs)
	}
}

func TestEmitJSON(t *testing.T) {
	w, out, _ := newTest(execctx.FormatJSON, false)
	if err := w.Emit(sample); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("emitted JSON does not parse: %v", err)
	}
	if len(got) != 2 || got[0]["page"] != "/" || got[1]["visitors"] != "87" {
		t.Fatalf("unexpected payload: %v", got)
	}
}

// TestEmitHumanHeaderOnlyOnTTY: a header is help for a person and noise for
// `awk`. Piped output carries rows only.
func TestEmitHumanHeaderOnlyOnTTY(t *testing.T) {
	wTTY, outTTY, _ := newTest(execctx.FormatHuman, true)
	if err := wTTY.Emit(sample); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stripANSI(outTTY.String()), "page") {
		t.Fatalf("a terminal should get a header, got %q", outTTY.String())
	}

	wPipe, outPipe, _ := newTest(execctx.FormatHuman, false)
	if err := wPipe.Emit(sample); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outPipe.String(), "page") {
		t.Fatalf("a pipe should get no header, got %q", outPipe.String())
	}
	if lines := strings.Split(strings.TrimRight(outPipe.String(), "\n"), "\n"); len(lines) != 2 {
		t.Fatalf("a pipe should get exactly the rows, got %q", outPipe.String())
	}
}

// TestNoTrailingWhitespace keeps output diffable and free of the invisible
// padding that makes a copied line look wrong.
func TestNoTrailingWhitespace(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	if err := w.Emit(sample); err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("line %d has trailing whitespace: %q", i, line)
		}
	}
}

func TestRawPassesThroughUntouched(t *testing.T) {
	w, out, _ := newTest(execctx.FormatJSON, false)
	payload := []byte(`{"results":[{"metrics":{"visitors":1}}]}`)
	if err := w.Raw(payload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("Raw reformatted the payload: %q", out.String())
	}
}

// TestEmitRecordAlignsIncludingTheColon caught a column that overflowed
// because the width was measured on the name and rendered with a colon.
func TestEmitRecordAlignsIncludingTheColon(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	err := w.EmitRecord(Record{
		{Name: "short", Value: "a"},
		{Name: "a_much_longer_name", Value: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	col := func(s string) int { return strings.Index(s, strings.TrimLeft(s[strings.Index(s, ":")+1:], " ")) }
	if col(lines[0]) != col(lines[1]) {
		t.Fatalf("values are not aligned:\n%q\n%q", lines[0], lines[1])
	}
}

// TestEmitRecordSameContentEveryFormat: a caller asking for CSV must not
// silently receive JSON, and no format may drop a field the others carry.
func TestEmitRecordSameContentEveryFormat(t *testing.T) {
	rec := Record{
		{Name: "authenticated", Value: true},
		{Name: "source", Value: "environment"},
	}

	wJSON, outJSON, _ := newTest(execctx.FormatJSON, false)
	if err := wJSON.EmitRecord(rec); err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(outJSON.Bytes(), &obj); err != nil {
		t.Fatalf("json does not parse: %v", err)
	}

	wCSV, outCSV, _ := newTest(execctx.FormatCSV, false)
	if err := wCSV.EmitRecord(rec); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(outCSV.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv does not parse (did it emit JSON instead?): %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("csv should be a header and one row, got %v", recs)
	}
	if len(recs[0]) != len(obj) {
		t.Fatalf("csv has %d columns but json has %d keys", len(recs[0]), len(obj))
	}
	for _, name := range recs[0] {
		if _, ok := obj[name]; !ok {
			t.Fatalf("column %q is missing from the json shape", name)
		}
	}
}

// TestEmitRecordSingleValueHasNoLabel: a label for one value is noise.
func TestEmitRecordSingleValueHasNoLabel(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	if err := w.EmitRecord(Record{{Name: "version", Value: "1.2.3"}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "1.2.3\n" {
		t.Fatalf("output = %q, want the bare value", got)
	}
}

// TestSingleVisibleFieldHasNoLabel: a record with one visible field and one
// machine-only field is still a single value to a person.
func TestSingleVisibleFieldHasNoLabel(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	err := w.EmitRecord(Record{
		{Name: "site_id", Value: 42, OmitHuman: true},
		{Name: "default_site", Value: "example.com", Human: "default site is now example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "default site is now example.com\n" {
		t.Fatalf("output = %q, want the bare sentence", got)
	}
}

// TestOmitHumanStillReachesMachineFormats: a field hidden from a person must
// still be in the JSON and CSV, or the formats carry different content.
func TestOmitHumanStillReachesMachineFormats(t *testing.T) {
	rec := Record{
		{Name: "site_id", Value: 42, OmitHuman: true},
		{Name: "name", Value: "example.com"},
	}
	wJSON, outJSON, _ := newTest(execctx.FormatJSON, false)
	if err := wJSON.EmitRecord(rec); err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(outJSON.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["site_id"]; !ok {
		t.Fatalf("a human-hidden field must still be in JSON: %v", obj)
	}
}

// TestNumericColumnsStayNumbersInJSON: a caller must be able to do arithmetic
// on a metric without parsing it first. Quoted numbers made `jq '.visitors+1'`
// fail on every reading command.
func TestNumericColumnsStayNumbersInJSON(t *testing.T) {
	w, out, _ := newTest(execctx.FormatJSON, false)
	err := w.Emit(Table{
		Columns: []string{"page", "visitors", "rate"},
		Rows:    [][]string{{"/", "1204", "41.2"}, {"/x", "", "0"}},
		Numeric: []int{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[0]["visitors"].(float64); !ok {
		t.Fatalf("visitors = %T, want a number", rows[0]["visitors"])
	}
	if _, ok := rows[0]["page"].(string); !ok {
		t.Fatalf("page = %T, want a string", rows[0]["page"])
	}
	// A metric the dimension did not compute is absent, not zero.
	if rows[1]["visitors"] != nil {
		t.Fatalf("an empty numeric cell should be null, got %v", rows[1]["visitors"])
	}
	if v, _ := rows[1]["rate"].(float64); v != 0 {
		t.Fatalf("a real zero must survive as zero, got %v", rows[1]["rate"])
	}
}

// TestControlCharactersCannotBreakTheTable: a page path or referrer is server
// data. Printed raw, a newline splits one row across two lines and every
// column after it stops lining up.
func TestControlCharactersCannotBreakTheTable(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	err := w.Emit(Table{
		Columns: []string{"page", "visitors"},
		Rows:    [][]string{{"a\nb\tc\x07", "1"}, {"/ok", "2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and two rows, got %d lines: %q", len(lines), out.String())
	}
	for i, l := range lines {
		if strings.ContainsAny(l, "\t\x07") {
			t.Fatalf("line %d still carries a control character: %q", i, l)
		}
	}
}

// TestMachineFormatsKeepValuesIntact: CSV quotes and JSON escapes, so neither
// needs the sanitising a terminal does, and stripping there would corrupt data.
func TestMachineFormatsKeepValuesIntact(t *testing.T) {
	raw := "a,b\"c\nd"
	wCSV, outCSV, _ := newTest(execctx.FormatCSV, false)
	if err := wCSV.Emit(Table{Columns: []string{"page"}, Rows: [][]string{{raw}}}); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(outCSV.String())).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v", err)
	}
	if recs[1][0] != raw {
		t.Fatalf("CSV round trip changed the value: %q", recs[1][0])
	}
}

// TestWidthIsMeasuredInRunes: a name in Cyrillic or with an em dash is fewer
// runes than bytes, and padding by bytes misaligns every following column.
func TestWidthIsMeasuredInRunes(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	err := w.Emit(Table{
		Columns: []string{"name", "n"},
		Rows:    [][]string{{"Ukraine", "1"}, {"Україна", "2"}},
		Right:   []int{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// The column position has to be counted in runes: a byte index would
	// differ for Cyrillic even when the rendering is correctly aligned, which
	// is the very confusion this test exists to rule out.
	col := func(s string) int {
		r := []rune(s)
		for i := len(r) - 1; i >= 0; i-- {
			if r[i] == ' ' {
				return i
			}
		}
		return -1
	}
	if col(lines[1]) != col(lines[2]) {
		t.Fatalf("columns misaligned across scripts:\n%q (%d)\n%q (%d)",
			lines[1], col(lines[1]), lines[2], col(lines[2]))
	}
}

// TestCSVNumbersAreNotScientific: %v on a float64 uses %g, so a site with a
// million pageviews produced "9.87e+09" in CSV while JSON carried the real
// number. A spreadsheet and awk both choke on the first.
func TestCSVNumbersAreNotScientific(t *testing.T) {
	w, out, _ := newTest(execctx.FormatCSV, false)
	err := w.EmitRecord(Record{
		{Name: "visitors", Value: float64(1234567)},
		{Name: "pageviews", Value: float64(9876543210)},
		{Name: "bounce_rate", Value: 41.25},
	})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// Only the data row: the header legitimately contains the letter e.
	for _, cell := range recs[1] {
		if strings.ContainsAny(cell, "eE") {
			t.Fatalf("cell %q is in scientific notation", cell)
		}
	}
	if recs[1][0] != "1234567" || recs[1][1] != "9876543210" || recs[1][2] != "41.25" {
		t.Fatalf("values = %v", recs[1])
	}
}

// TestNullMetricIsAnEmptyCellNotTheWordNil: a rate may be null when it is
// undefined for the window, and "<nil>" is not a number any consumer expects.
func TestNullMetricIsAnEmptyCellNotTheWordNil(t *testing.T) {
	for _, f := range []execctx.Format{execctx.FormatCSV, execctx.FormatHuman} {
		w, out, _ := newTest(f, false)
		if err := w.EmitRecord(Record{
			{Name: "visitors", Value: float64(10)},
			{Name: "bounce_rate", Value: nil},
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "nil") {
			t.Fatalf("%s: %q", f, out.String())
		}
	}
	w, out, _ := newTest(execctx.FormatJSON, false)
	if err := w.EmitRecord(Record{{Name: "bounce_rate", Value: nil}}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if v, present := m["bounce_rate"]; !present || v != nil {
		t.Fatalf("JSON should carry an explicit null, got %v", m)
	}
}

// TestColourIsAppliedAndSuppressed. Before this the whole colour machinery —
// the --no-color flag, NO_COLOR, FORCE_COLOR and Context.UseColor — was
// computed, threaded through and unit-tested in isolation, while nothing in
// the CLI ever emitted an escape sequence. The flags described an intention
// the code never acted on.
func TestColourIsAppliedAndSuppressed(t *testing.T) {
	table := Table{Columns: []string{"page", "n"}, Rows: [][]string{{"/", "1"}}}

	styled := func(ctx execctx.Context) string {
		var out, errb bytes.Buffer
		w := New(ctx, &out, &errb)
		if err := w.Emit(table); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	on := styled(execctx.Context{Format: execctx.FormatHuman, StdoutTTY: true})
	if !strings.Contains(on, "\x1b[") {
		t.Fatalf("an interactive terminal should get styling, got %q", on)
	}
	if stripANSI(on) != "page  n\n/     1\n" {
		t.Fatalf("styling changed the content: %q", stripANSI(on))
	}

	for _, tc := range []struct {
		name string
		ctx  execctx.Context
	}{
		{"--no-color", execctx.Context{Format: execctx.FormatHuman, StdoutTTY: true, NoColor: true}},
		{"not a terminal", execctx.Context{Format: execctx.FormatHuman}},
		{"json", execctx.Context{Format: execctx.FormatJSON, StdoutTTY: true}},
		{"csv", execctx.Context{Format: execctx.FormatCSV, StdoutTTY: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(styled(tc.ctx), "\x1b[") {
				t.Fatalf("%s must produce no escape sequences", tc.name)
			}
		})
	}
}

// TestHeaderWidthDoesNotLeakIntoAPipe. A pipe gets no header, so a header must
// not set a column's width there. It did, so every row was padded out to the
// width of a title nobody could see, and any row whose last cell was empty
// ended in that padding.
func TestHeaderWidthDoesNotLeakIntoAPipe(t *testing.T) {
	table := Table{
		Columns: []string{"id", "a_very_long_column_title", "default"},
		Rows: [][]string{
			{"1", "x", "*"},
			{"2", "y", ""},
		},
	}

	w, out, _ := newTest(execctx.FormatHuman, false)
	if err := w.Emit(table); err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("line %d ends in whitespace: %q", i, line)
		}
		if strings.Contains(line, "     ") {
			t.Fatalf("line %d is padded to an invisible header: %q", i, line)
		}
	}

	// On a terminal the header is shown, so it legitimately sets the width.
	wt, outt, _ := newTest(execctx.FormatHuman, true)
	if err := wt.Emit(table); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(outt.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "id") {
		t.Fatalf("a terminal should still get an aligned header: %q", outt.String())
	}
	for i, line := range lines {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("terminal line %d ends in whitespace: %q", i, line)
		}
	}
}

// TestTrailingEmptyCellsAreDropped: a marker column empty for all but one row
// must not leave every other row ending in the padding of a column that had
// nothing in it.
func TestTrailingEmptyCellsAreDropped(t *testing.T) {
	w, out, _ := newTest(execctx.FormatHuman, true)
	err := w.Emit(Table{
		Columns: []string{"name", "mark"},
		Rows:    [][]string{{"one", "*"}, {"two", ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("line %d ends in whitespace: %q", i, line)
		}
	}
	// CSV keeps every field: dropping one there would change the row shape.
	wc, outc, _ := newTest(execctx.FormatCSV, false)
	if err := wc.Emit(Table{
		Columns: []string{"name", "mark"},
		Rows:    [][]string{{"two", ""}},
	}); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(outc.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs[1]) != 2 {
		t.Fatalf("CSV must keep the empty field, got %v", recs[1])
	}
}

// TestBoolColumnCarriesTheSameFactToEveryFormat: a marker column is a display
// choice, and a machine reading "*" has to guess what the empty cell means.
func TestBoolColumnCarriesTheSameFactToEveryFormat(t *testing.T) {
	table := func() Table {
		return Table{
			Columns: []string{"name", "default"},
			Rows:    [][]string{{"a.example", "true"}, {"b.example", ""}},
			Bool:    []int{1},
		}
	}

	w, out, _ := newTest(execctx.FormatJSON, false)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out.String())
	}
	if rows[0]["default"] != true || rows[1]["default"] != false {
		t.Fatalf("JSON must carry real booleans, got %v and %v",
			rows[0]["default"], rows[1]["default"])
	}

	w, out, _ = newTest(execctx.FormatCSV, false)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// An empty cell would read as missing in a spreadsheet, not as false.
	if recs[1][1] != "true" || recs[2][1] != "false" {
		t.Fatalf("CSV must spell both values, got %q and %q", recs[1][1], recs[2][1])
	}

	w, out, _ = newTest(execctx.FormatHuman, true)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	human := stripANSI(out.String())
	if strings.Contains(human, "false") {
		t.Fatalf("a column of the word false is noise a reader has to skip:\n%s", human)
	}
	if !strings.Contains(human, boolMark) {
		t.Fatalf("the true row lost its mark:\n%s", human)
	}
}

// TestHumanOmitHidesColumnsFromPeopleOnly: the terminal table stays readable
// while the machine formats keep everything the endpoint returned.
func TestHumanOmitHidesColumnsFromPeopleOnly(t *testing.T) {
	table := func() Table {
		return Table{
			Columns:   []string{"name", "hash", "created_at"},
			Rows:      [][]string{{"a.example", "deadbeef", "2026-01-01"}},
			HumanOmit: []int{1, 2},
		}
	}

	w, out, _ := newTest(execctx.FormatHuman, true)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "deadbeef") {
		t.Fatalf("an omitted column reached the terminal:\n%s", out.String())
	}

	w, out, _ = newTest(execctx.FormatJSON, false)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deadbeef") {
		t.Fatalf("JSON lost a column it must keep:\n%s", out.String())
	}
}

// TestForHumanKeepsAlignmentIndicesInRange: Right holds column positions, and
// dropping a column shifts every later one. An index left pointing past the
// end silently right-aligns the wrong column, or none.
func TestForHumanKeepsAlignmentIndicesInRange(t *testing.T) {
	got := forHuman(Table{
		Columns:   []string{"a", "b", "c", "d"},
		Rows:      [][]string{{"1", "2", "3", "4"}},
		Right:     []int{0, 2, 3},
		HumanOmit: []int{1},
	})
	if len(got.Columns) != 3 {
		t.Fatalf("columns = %v", got.Columns)
	}
	for _, i := range got.Right {
		if i < 0 || i >= len(got.Columns) {
			t.Fatalf("Right index %d is outside %d columns", i, len(got.Columns))
		}
	}
	// a, c and d survive at positions 0, 1 and 2.
	want := []int{0, 1, 2}
	if len(got.Right) != len(want) {
		t.Fatalf("Right = %v, want %v", got.Right, want)
	}
	for i := range want {
		if got.Right[i] != want[i] {
			t.Fatalf("Right = %v, want %v", got.Right, want)
		}
	}
	if len(got.Rows[0]) != 3 {
		t.Fatalf("row = %v", got.Rows[0])
	}
}

// TestForHumanToleratesShortRows: a row shorter than the header is a caller
// bug, but it must not panic in the renderer.
func TestForHumanToleratesShortRows(t *testing.T) {
	got := forHuman(Table{
		Columns:   []string{"a", "b", "c"},
		Rows:      [][]string{{"1"}, {}, {"1", "2", "3"}},
		Bool:      []int{2},
		HumanOmit: []int{0},
	})
	for _, r := range got.Rows {
		if len(r) != len(got.Columns) {
			t.Fatalf("row %v does not match %d columns", r, len(got.Columns))
		}
	}
}

// TestNotesAndWarningsCannotForgeALine is the reason sanitising lives in the
// writer rather than at each call site.
//
// A note routinely carries text this program did not write: a site name, a
// funnel name, a path, an error from a parser fed someone else's file. One
// forgotten call site let a funnel named with an embedded newline print a
// second line an operator would read as real, followed by a live escape
// sequence that leaked colour into the rest of the session.
func TestNotesAndWarningsCannotForgeALine(t *testing.T) {
	evil := "Checkout\nFAKE: 999 of 999 visitors entered\x1b[31m\x07"

	for _, tc := range []struct {
		name  string
		write func(w *Writer)
	}{
		{"note", func(w *Writer) { w.Note("%s: done", evil) }},
		{"warn", func(w *Writer) { w.Warn("%s: careful", evil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, errb := newTest(execctx.FormatHuman, true)
			tc.write(w)

			got := errb.String()
			if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
				t.Fatalf("server text forged %d extra line(s): %q", n, got)
			}
			if strings.ContainsRune(got, 0x1b) {
				t.Fatalf("an escape sequence reached the terminal: %q", got)
			}
			if strings.ContainsRune(got, 0x07) {
				t.Fatalf("a control character reached the terminal: %q", got)
			}
			// The words still have to be readable: sanitising must not
			// swallow the message, only its ability to control the terminal.
			if !strings.Contains(got, "Checkout") {
				t.Fatalf("the text itself was lost: %q", got)
			}
		})
	}
}

// TestRowsAlwaysMatchTheHeader: a table's Columns and its Rows are built in
// separate loops and nothing makes them agree. A short row used to write a CSV
// record narrower than its header — a file Go's own csv.Reader rejects — and
// silently drop a boolean cell from JSON; a long one wrote extra CSV fields
// and lost the same values in JSON.
func TestRowsAlwaysMatchTheHeader(t *testing.T) {
	table := func() Table {
		return Table{
			Columns: []string{"a", "b", "c"},
			Rows:    [][]string{{"1"}, {}, {"1", "true", "3"}, {"1", "true", "3", "extra"}},
			Bool:    []int{1},
		}
	}

	w, out, _ := newTest(execctx.FormatCSV, false)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, out.String())
	}
	for i, r := range recs {
		if len(r) != 3 {
			t.Fatalf("record %d has %d fields, want 3: %v", i, len(r), r)
		}
	}

	w, out, _ = newTest(execctx.FormatJSON, false)
	if err := w.Emit(table()); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if len(r) != 3 {
			t.Fatalf("object %d has %d keys, want 3: %v", i, len(r), r)
		}
		if _, ok := r["b"]; !ok {
			t.Fatalf("object %d lost its boolean column: %v", i, r)
		}
	}
}

// TestEmitDoesNotMutateTheCallersTable: a boolean column is rewritten for CSV,
// and doing that in place would change a table the caller still holds and may
// emit again in another format.
func TestEmitDoesNotMutateTheCallersTable(t *testing.T) {
	tbl := Table{
		Columns: []string{"name", "default"},
		Rows:    [][]string{{"a.example", "true"}, {"b.example", ""}},
		Bool:    []int{1},
	}
	w, _, _ := newTest(execctx.FormatCSV, false)
	if err := w.Emit(tbl); err != nil {
		t.Fatal(err)
	}
	if tbl.Rows[0][1] != "true" || tbl.Rows[1][1] != "" {
		t.Fatalf("the caller's rows were rewritten: %v", tbl.Rows)
	}
}
