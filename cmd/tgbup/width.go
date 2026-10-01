package main

import (
	"strings"
	"unicode"
)

// displayWidth estimates how many terminal cells s takes: East Asian wide
// characters and emoji take two, combining marks, variation selectors and
// the parts of a ZWJ emoji sequence after the first take none, and a flag
// (two regional indicators) takes two.
func displayWidth(s string) int {
	w := 0
	afterZWJ := false
	for _, r := range s {
		switch {
		case r == '‍':
			afterZWJ = true
			continue
		case afterZWJ:
			afterZWJ = false
			continue
		}
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r),
		r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return 0
	case r >= 0x1f1e6 && r <= 0x1f1ff: // regional indicator: a flag is a pair
		return 1
	case isWide(r):
		return 2
	}
	return 1
}

func isWide(r rune) bool {
	for _, rg := range wideRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// wideRanges covers East Asian Wide/Fullwidth blocks and emoji shown with
// emoji presentation by default.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3},
	{0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1},
	{0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea},
	{0x26f2, 0x26f3}, {0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b},
	{0x2728, 0x2728}, {0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2764, 0x2764},
	{0x2795, 0x2797}, {0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf}, {0xac00, 0xd7a3},
	{0xf900, 0xfaff}, {0xfe30, 0xfe4f}, {0xff00, 0xff60}, {0xffe0, 0xffe6},
	{0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf}, {0x1f18e, 0x1f18e}, {0x1f191, 0x1f19a}, {0x1f200, 0x1f251},
	{0x1f300, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f7e0, 0x1f7eb}, {0x1f90c, 0x1f9ff}, {0x1fa70, 0x1faff},
	{0x20000, 0x3fffd},
}

// padRight pads s with spaces to width cells; longer strings are left as is.
func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-displayWidth(s), 0))
}

// truncateWidth cuts s to at most width cells, ending with "…" when cut. A
// ZWJ emoji sequence is dropped whole rather than split.
func truncateWidth(s string, width int) string {
	if displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	for _, cluster := range clusters(s) {
		if displayWidth(b.String())+displayWidth(cluster) > width-1 {
			break
		}
		b.WriteString(cluster)
	}
	return b.String() + "…"
}

// clusters splits s into runs that must stay together: a base rune with the
// combining marks, variation selectors and ZWJ continuations that follow it,
// and regional indicator pairs.
func clusters(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); {
		j := clusterEnd(rs, i)
		out = append(out, string(rs[i:j]))
		i = j
	}
	return out
}

func clusterEnd(rs []rune, i int) int {
	isRI := func(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }
	j := i + 1
	if isRI(rs[i]) && j < len(rs) && isRI(rs[j]) {
		j++
	}
	for j < len(rs) {
		switch {
		case rs[j] == '\u200d' && j+1 < len(rs):
			j += 2
		case runeWidth(rs[j]) == 0:
			j++
		default:
			return j
		}
	}
	return j
}
