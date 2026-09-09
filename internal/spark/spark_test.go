package spark

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmptyIsEmpty(t *testing.T) {
	if got := Render(nil, Blocks); got != "" {
		t.Fatalf("Render(nil) = %q", got)
	}
}

func TestOneColumnPerPoint(t *testing.T) {
	for _, m := range []Marker{Blocks, ASCII} {
		got := Render([]float64{1, 2, 3, 4, 5}, m)
		if n := utf8.RuneCountInString(got); n != 5 {
			t.Fatalf("%s: %d columns for 5 points (%q)", m, n, got)
		}
	}
}

// TestScaleStartsAtZero: scaling from the minimum turns a flat week into a
// dramatic one, which is the most common way a sparkline lies.
func TestScaleStartsAtZero(t *testing.T) {
	flat := Render([]float64{100, 101, 100, 99, 100}, Blocks)
	runes := []rune(flat)
	for i := 1; i < len(runes); i++ {
		if runes[i] != runes[0] {
			t.Fatalf("a nearly flat series should look flat, got %q", flat)
		}
	}

	real := Render([]float64{0, 50, 100}, Blocks)
	rr := []rune(real)
	if rr[0] == rr[2] {
		t.Fatalf("a real climb should not look flat, got %q", real)
	}
}

// TestAllZeroIsDrawn: a week with no traffic is information; a blank line
// looks like a bug.
func TestAllZeroIsDrawn(t *testing.T) {
	got := Render([]float64{0, 0, 0}, Blocks)
	if utf8.RuneCountInString(got) != 3 {
		t.Fatalf("all-zero series should still draw, got %q", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatalf("all-zero series drew nothing visible: %q", got)
	}
}

func TestPeakUsesTheTallestGlyph(t *testing.T) {
	got := []rune(Render([]float64{1, 10}, Blocks))
	if got[1] != blockRunes[len(blockRunes)-1] {
		t.Fatalf("the peak should use the tallest glyph, got %q", string(got))
	}
}

// TestNonFiniteValuesDoNotPanic: a rate can be null for a window, and JSON
// decoding of an absent number can produce NaN downstream.
func TestNonFiniteValuesDoNotPanic(t *testing.T) {
	got := Render([]float64{1, math.NaN(), math.Inf(1), math.Inf(-1), 2}, Blocks)
	if utf8.RuneCountInString(got) != 5 {
		t.Fatalf("every point must still occupy a column, got %q", got)
	}
}

func TestNegativeValuesFloorAtZero(t *testing.T) {
	got := []rune(Render([]float64{-5, 10}, Blocks))
	if got[0] != blockRunes[0] {
		t.Fatalf("a negative value should sit on the floor, got %q", string(got))
	}
}

func TestBrailleHalvesTheWidth(t *testing.T) {
	got := Render([]float64{1, 2, 3, 4, 5, 6}, Braille)
	if n := utf8.RuneCountInString(got); n != 3 {
		t.Fatalf("braille packs two points per cell, got %d cells for 6 points", n)
	}
	for _, r := range got {
		if r < 0x2800 || r > 0x28FF {
			t.Fatalf("not a braille glyph: %q", r)
		}
	}
}

func TestUnknownMarkerFallsBackToBlocks(t *testing.T) {
	if Render([]float64{1, 2}, Marker("crayon")) != Render([]float64{1, 2}, Blocks) {
		t.Fatal("an unknown marker should fall back rather than draw nothing")
	}
	if Marker("crayon").Valid() {
		t.Fatal("an unknown marker must not validate")
	}
}

// TestASCIIStaysASCII: the fallback exists for terminals and pipes that cannot
// be trusted above U+007F, so it must not emit anything above it.
func TestASCIIStaysASCII(t *testing.T) {
	got := Render([]float64{0, 1, 2, 3, 4, 5, 6, 7, 8}, ASCII)
	for _, r := range got {
		if r > 127 {
			t.Fatalf("ASCII marker emitted %q", r)
		}
	}
}
