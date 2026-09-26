package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/charmbracelet/x/ansi"
)

// truncate cuts s to n display cells, measuring wide characters and ANSI
// sequences correctly.
func truncate(s string, n int) string {
	if n <= 1 || ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanize(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// humanizeMoney keeps big totals short; cents only matter on small ones.
func humanizeMoney(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", v/1e3)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

// row assembles a table line from styled segments. Rendering the segments
// inside one outer style would not work: each inner style ends with a reset,
// which also clears the selection fill for the rest of the line. Instead every
// segment carries the fill itself.
type row struct {
	picked bool
	b      strings.Builder
}

func (r *row) add(s lipgloss.Style, text string) {
	if r.picked {
		s = s.Background(pickColor).Bold(true)
	}
	r.b.WriteString(s.Render(text))
}

// render pads the line to width so a selected row is filled edge to edge.
func (r *row) render(width int) string {
	line := truncate(r.b.String(), width)
	if gap := width - lipgloss.Width(line); gap > 0 && r.picked {
		line += pickStyle.Render(strings.Repeat(" ", gap))
	}
	return line
}
