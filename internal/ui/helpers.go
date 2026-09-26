package ui

import (
	"fmt"

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
