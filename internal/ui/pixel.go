package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// glyphs is a 5-pixel-tall font for numbers, drawn with half blocks into
// three terminal rows, the last of them half filled. A glyph is as wide as
// its rows.
var glyphs = map[rune][5]string{
	'0': {"###", "# #", "# #", "# #", "###"},
	'1': {" # ", "## ", " # ", " # ", "###"},
	'2': {"###", "  #", "###", "#  ", "###"},
	'3': {"###", "  #", "###", "  #", "###"},
	'4': {"# #", "# #", "###", "  #", "  #"},
	'5': {"###", "#  ", "###", "  #", "###"},
	'6': {"###", "#  ", "###", "# #", "###"},
	'7': {"###", "  #", "  #", "  #", "  #"},
	'8': {"###", "# #", "###", "# #", "###"},
	'9': {"###", "# #", "###", "  #", "###"},
	'.': {" ", " ", " ", " ", "#"},
	'-': {"   ", "   ", "###", "   ", "   "},
}

// pixelRows is how many terminal rows a pixel number takes.
const pixelRows = 3

// pixels draws s in the pixel font, three rows of half blocks, glyphs a
// column apart. A rune the font lacks is left out.
func pixels(s string, style lipgloss.Style) [pixelRows]string {
	var rows [pixelRows]strings.Builder
	first := true
	for _, r := range s {
		g, ok := glyphs[r]
		if !ok {
			continue
		}
		if !first {
			for i := range rows {
				rows[i].WriteByte(' ')
			}
		}
		first = false
		for i := range rows {
			bottom := strings.Repeat(" ", len(g[0]))
			if 2*i+1 < len(g) {
				bottom = g[2*i+1]
			}
			rows[i].WriteString(halfBlocks(g[2*i], bottom))
		}
	}
	var out [pixelRows]string
	for i := range rows {
		out[i] = style.Render(rows[i].String())
	}
	return out
}

// halfBlocks folds two pixel rows into one row of text.
func halfBlocks(top, bottom string) string {
	var b strings.Builder
	for i := range top {
		switch t, u := top[i] == '#', bottom[i] == '#'; {
		case t && u:
			b.WriteString("█")
		case t:
			b.WriteString("▀")
		case u:
			b.WriteString("▄")
		default:
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// ticks is a bar of thin ticks, width cells wide, the first share of them
// in on and the rest in off. Anything above zero shows at least one tick.
func ticks(share float64, width int, on, off lipgloss.Style) string {
	share = max(0, min(share, 1))
	n := int(share*float64(width) + 0.5)
	if share > 0 && n == 0 {
		n = 1
	}
	return on.Render(strings.Repeat("▍", n)) + off.Render(strings.Repeat("▍", width-n))
}
