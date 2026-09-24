package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/usage"
	"github.com/charmbracelet/lipgloss"
)

// Heatmap palette: cool colors for low volume, warm for high, so the heaviest
// days stand out at a glance.
var heatLevels = []lipgloss.Color{
	lipgloss.Color("236"), // 0: no record
	lipgloss.Color("22"),  // 1
	lipgloss.Color("28"),  // 2
	lipgloss.Color("34"),  // 3
	lipgloss.Color("40"),  // 4
	lipgloss.Color("46"),  // 5
	lipgloss.Color("82"),  // 6
	lipgloss.Color("118"), // 7
	lipgloss.Color("154"), // 8
	lipgloss.Color("190"), // 9
	lipgloss.Color("226"), // 10
	lipgloss.Color("220"), // 11
	lipgloss.Color("214"), // 12
	lipgloss.Color("208"), // 13
	lipgloss.Color("202"), // 14: highest
}

// heatCell is a single heatmap cell.
type heatCell struct {
	Date   string
	Tokens int64
	Cost   float64
	Level  int
}

// buildHeatmap turns the per-date aggregates into a GitHub-style calendar
// heatmap: one column per week, one row per weekday, starting on the Sunday of
// the earliest record.
func buildHeatmap(days []usage.DateRow) []heatCell {
	byDate := map[string]usage.DateRow{}
	for _, d := range days {
		byDate[d.Date] = d
	}
	if len(byDate) == 0 {
		return nil
	}

	// Find the maximum token count, used for leveling.
	var max int64
	for _, d := range byDate {
		if d.Tokens > max {
			max = d.Tokens
		}
	}

	// Start on the Sunday of the earliest date and fill every day up to today.
	first := earliestDate(byDate)
	last := latestDate(byDate)
	start := startOfWeek(first)

	cells := []heatCell{}
	for d := start; !d.After(last); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		row, ok := byDate[key]
		c := heatCell{Date: key}
		if ok {
			c.Tokens = row.Tokens
			c.Cost = row.Cost
			c.Level = levelFor(row.Tokens, max)
		}
		cells = append(cells, c)
	}
	// Pad to a whole week, otherwise the trailing days are lost out of bounds
	// during week-based rendering.
	for len(cells)%7 != 0 {
		last := mustDate(cells[len(cells)-1].Date)
		cells = append(cells, heatCell{Date: last.AddDate(0, 0, 1).Format("2006-01-02")})
	}
	return cells
}

func levelFor(v, max int64) int {
	if v <= 0 {
		return 0
	}
	// Logarithmic leveling: usage spans orders of magnitude (hundreds of
	// thousands to billions), and linear leveling would crowd almost every cell
	// into level 1.
	const steps = 14
	ratio := float64(v) / float64(max)
	lv := int(ratio10(ratio) * steps)
	// Non-zero usage gets at least level 1, otherwise it cannot be told apart
	// from a day with no record.
	if lv < 1 {
		lv = 1
	}
	if lv > steps {
		lv = steps
	}
	return lv
}

// ratio10 maps a ratio in (0,1] to (0,1].
//
// Usage spans orders of magnitude (hundreds of thousands to billions, a factor
// of tens of thousands), so leveling must be logarithmic; otherwise almost
// every cell lands on level 1 and the heatmap shows no difference.
// The floor is 1e-6: only ratios below a millionth of the maximum fall to zero.
// A return of 0 means "barely visible" — levelFor guarantees non-zero usage
// still gets level 1.
func ratio10(ratio float64) float64 {
	if ratio >= 1 {
		return 1
	}
	const floor = 1e-6
	if ratio <= floor {
		return 0
	}
	// Map [floor,1] logarithmically onto (0,1].
	v := (math.Log10(ratio) - math.Log10(floor)) / -math.Log10(floor)
	if v <= 0 {
		return 0
	}
	return v
}

func mustDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func earliestDate(m map[string]usage.DateRow) time.Time {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t, _ := time.Parse("2006-01-02", keys[0])
	return t
}

func latestDate(m map[string]usage.DateRow) time.Time {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t, _ := time.Parse("2006-01-02", keys[len(keys)-1])
	return t
}

// startOfWeek returns the Sunday of the week containing t.
func startOfWeek(t time.Time) time.Time {
	wd := int(t.Weekday()) // 0 = Sunday
	return t.AddDate(0, 0, -wd)
}

// renderHeatmap renders the heatmap. Each row is a weekday, each column a week.
func renderHeatmap(cells []heatCell) string {
	if len(cells) == 0 {
		return "  no data\n"
	}

	weekdayLabels := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	var b strings.Builder

	// Row label style.
	labelStyle := lipgloss.NewStyle().Faint(true).Width(3)
	monthStyle := lipgloss.NewStyle().Faint(true)

	// Render row by row: 7 rows (Sunday through Saturday).
	weeks := (len(cells) + 6) / 7
	for wd := 0; wd < 7; wd++ {
		b.WriteString(labelStyle.Render(weekdayLabels[wd][:1]))
		b.WriteString(" ")

		// The month label row is inserted before Wednesday, matching GitHub.
		if wd == 1 {
			lastMonth := ""
			for w := 0; w < weeks; w++ {
				idx := w*7 + wd
				m := ""
				if idx < len(cells) {
					t, _ := time.Parse("2006-01-02", cells[idx].Date)
					m = fmt.Sprintf("%-3d", int(t.Month()))
					if m == lastMonth {
						m = "   "
					} else {
						lastMonth = m
					}
				} else {
					m = "   "
				}
				b.WriteString(monthStyle.Render(strings.TrimLeft(m, " ")))
				b.WriteString(" ")
			}
			b.WriteString("\n")
			b.WriteString(labelStyle.Render(" "))
			b.WriteString(" ")
		}

		for w := 0; w < weeks; w++ {
			idx := w*7 + wd
			if idx >= len(cells) {
				b.WriteString(" ")
				b.WriteString(" ")
				continue
			}
			c := cells[idx]
			style := lipgloss.NewStyle().Foreground(heatLevels[c.Level])
			// Use a block character rather than a space: spaces are invisible on
			// some terminal backgrounds, color blocks are more legible.
			b.WriteString(style.Render("██"))
		}
		b.WriteString("\n")
	}

	// Legend.
	b.WriteString("\n  less ")
	for i := 1; i <= 14; i += 3 {
		b.WriteString(lipgloss.NewStyle().Foreground(heatLevels[i]).Render("██"))
	}
	b.WriteString(" more\n")
	return b.String()
}
