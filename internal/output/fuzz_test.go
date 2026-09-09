package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/key-arg/statable-cli/internal/execctx"
)

// FuzzEmit drives the writer with values that came from a server: page paths,
// referrers, custom event names. Two properties have to hold whatever the
// bytes are, because both are what downstream tooling relies on: CSV must
// round-trip, and human output must stay one line per row.
func FuzzEmit(f *testing.F) {
	for _, seed := range []string{
		"/", "/pricing", "a,b", `a"b`, "a\nb", "a\tb", "a\x00b",
		"\u0423\u043a\u0440\u0430\u0457\u043d\u0430", "\u65e5\u672c", "\U0001F642",
		"a\u0085b", "a\u2028b", "a\u202eb", "a\u009bb",
		"", " ", strings.Repeat("x", 300),
	} {
		f.Add(seed, "1204")
	}
	f.Fuzz(func(t *testing.T, cell, n string) {
		table := Table{
			Columns: []string{"page", "visitors"},
			Rows:    [][]string{{cell, n}},
			Right:   []int{1},
		}

		// CSV has to survive a round trip unchanged: it is the format a
		// spreadsheet and awk read, and a value that does not come back is a
		// silently corrupted export.
		var out, errb bytes.Buffer
		w := New(execctx.Context{Format: execctx.FormatCSV}, &out, &errb)
		if err := w.Emit(table); err != nil {
			t.Fatal(err)
		}
		if utf8.ValidString(cell) {
			recs, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
			if err != nil {
				t.Fatalf("emitted CSV does not parse: %v", err)
			}
			// Go's CSV reader normalises a CRLF inside a quoted field to a
			// bare LF. That is the reader's choice, not a defect in what was
			// written: the field is correctly quoted and other readers keep
			// both bytes. The comparison is made on the same normalisation so
			// the test measures the writer rather than the reader.
			want := strings.ReplaceAll(cell, "\r\n", "\n")
			if len(recs) != 2 || recs[1][0] != want {
				t.Fatalf("CSV round trip changed %q into %q", cell, recs[1][0])
			}
		}

		// JSON must stay parseable.
		out.Reset()
		wj := New(execctx.Context{Format: execctx.FormatJSON}, &out, &errb)
		if err := wj.Emit(table); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out.Bytes(), &[]map[string]any{}); err != nil {
			t.Fatalf("emitted JSON does not parse: %v", err)
		}

		// Human output must stay one line per row, or the table stops being a
		// table the moment a value carries a line break.
		out.Reset()
		wh := New(execctx.Context{Format: execctx.FormatHuman, NoColor: true}, &out, &errb)
		if err := wh.Emit(table); err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(strings.TrimRight(out.String(), "\n"), "\n"); got != 0 {
			t.Fatalf("one row produced %d line breaks for %q", got+1, cell)
		}
	})
}

// FuzzSanitize: whatever goes in, nothing that can move the cursor, break a
// line or reorder the text may come out.
func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{
		"", "ok", "a\nb", "\x1b[31m", " ", "\u202e", "\U0001F642", "\x00", "\u009b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := sanitize(in)
		for _, r := range got {
			if unsafeForCell(r) {
				t.Fatalf("sanitize left %U in %q", r, got)
			}
		}
		if utf8.ValidString(in) && !utf8.ValidString(got) {
			t.Fatalf("sanitize turned valid UTF-8 into invalid: %q", got)
		}
	})
}
