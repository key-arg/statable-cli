// Package spark draws a one-line chart of a single series.
//
// It is hand-written rather than pulled from a charting library. A single
// series on one line is a mapping from value to one of eight glyphs; a
// dependency would carry axes, multiple series and a rendering loop we do not
// use. When multi-series charts arrive, ntcharts is the library to reach for.
//
// Unicode blocks are the default and braille is opt-in, deliberately. A
// braille cell packs eight dots but carries exactly one foreground colour, so
// two series that cross must share a colour, and the blank braille glyph
// U+2800 is a real character rather than a space: it breaks grep, diff, and
// pasting a chart into an issue or a chat message.
package spark

import (
	"math"
	"strings"
)

// Marker selects the glyph set.
type Marker string

const (
	// Blocks is the default: one column per point, eight height levels.
	Blocks Marker = "blocks"
	// Braille packs two points per cell, at the cost of the drawbacks in the
	// package comment.
	Braille Marker = "braille"
	// ASCII is the fallback for a terminal or a pipe that cannot be trusted
	// with anything above U+007F.
	ASCII Marker = "ascii"
)

// Valid reports whether m names a marker this package knows.
func (m Marker) Valid() bool {
	switch m {
	case Blocks, Braille, ASCII:
		return true
	}
	return false
}

var blockRunes = []rune("▁▂▃▄▅▆▇█")
var asciiRunes = []rune("_.-~=+*#")

// Render draws values as a single line.
//
// The scale runs from zero rather than from the minimum. A chart of visitor
// counts that starts at the minimum turns a flat week into a dramatic one,
// which is the most common way a sparkline lies.
func Render(values []float64, m Marker) string {
	if len(values) == 0 {
		return ""
	}
	if !m.Valid() {
		m = Blocks
	}
	if m == Braille {
		return renderBraille(values)
	}

	runes := blockRunes
	if m == ASCII {
		runes = asciiRunes
	}

	peak := 0.0
	for _, v := range values {
		if !finite(v) {
			continue
		}
		if v > peak {
			peak = v
		}
	}

	var b strings.Builder
	b.Grow(len(values) * 3)
	for _, v := range values {
		b.WriteRune(runes[level(v, peak, len(runes))])
	}
	return b.String()
}

// level maps a value onto 0..steps-1. A peak of zero means every point is
// zero, which is drawn as the lowest glyph rather than as an empty line: a
// week with no traffic is information, and a blank line looks like a bug.
func level(v, peak float64, steps int) int {
	if !finite(v) || v <= 0 || peak <= 0 {
		return 0
	}
	idx := int(math.Round(v / peak * float64(steps-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= steps {
		idx = steps - 1
	}
	return idx
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// braille dot bits, by column then row, per the Unicode braille pattern
// layout: the low six dots are laid out in columns, and dots 7 and 8 sit at
// the bottom of each column.
var brailleBits = [2][4]rune{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

func renderBraille(values []float64) string {
	peak := 0.0
	for _, v := range values {
		if finite(v) && v > peak {
			peak = v
		}
	}

	var b strings.Builder
	for i := 0; i < len(values); i += 2 {
		var pattern rune
		for col := 0; col < 2 && i+col < len(values); col++ {
			h := level(values[i+col], peak, 4) + 1
			for row := 0; row < h; row++ {
				pattern |= brailleBits[col][3-row]
			}
		}
		b.WriteRune(0x2800 + pattern)
	}
	return b.String()
}
