package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/usage"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// heatSteps is the number of non-empty shades. Four steps, as on GitHub, is
// enough to read a ranking off the grid and few enough that neighbouring shades
// stay distinct; the old 14-step green-yellow-orange ramp was a rainbow, which
// reads as several categories rather than one increasing quantity.
const heatSteps = 4

// heatLevels is a single-hue sequential ramp, light to dark on a light terminal
// and dark to light on a dark one. Index 0 is "no record".
//
// Colours are applied as backgrounds, so the ramp must be legible against
// either terminal background; AdaptiveColor picks the matching set.
var heatLevels = []lipgloss.TerminalColor{
	lipgloss.AdaptiveColor{Light: "#ebedf0", Dark: "#232830"}, // 0: no record
	lipgloss.AdaptiveColor{Light: "#aceebb", Dark: "#0e4429"},
	lipgloss.AdaptiveColor{Light: "#4ac26b", Dark: "#006d32"},
	lipgloss.AdaptiveColor{Light: "#2da44e", Dark: "#26a641"},
	lipgloss.AdaptiveColor{Light: "#116329", Dark: "#4ae168"},
}

// heatShades is the fallback for a terminal with no colour: a background-only
// cell would render as blank space there and the whole grid would disappear.
var heatShades = []string{" ", "░", "▒", "▓", "█"}

// heatCell is a single day in the grid.
type heatCell struct {
	Date   string
	Tokens int64
	Cost   float64
	Level  int
}

// buildHeatmap turns per-date aggregates into a GitHub-style calendar: one
// column per week, one row per weekday, starting on the Sunday of the earliest
// record and running to the latest.
func buildHeatmap(days []usage.DateRow) []heatCell {
	byDate := map[string]usage.DateRow{}
	for _, d := range days {
		byDate[d.Date] = d
	}
	if len(byDate) == 0 {
		return nil
	}

	lv := newLeveler(byDate)
	first := earliestDate(byDate)
	last := latestDate(byDate)

	cells := []heatCell{}
	for d := startOfWeek(first); !d.After(last); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		c := heatCell{Date: key}
		if row, ok := byDate[key]; ok {
			c.Tokens = row.Tokens
			c.Cost = row.Cost
			c.Level = lv.level(row.Tokens)
		}
		cells = append(cells, c)
	}
	// Pad to a whole week, otherwise the trailing days fall out of bounds
	// during week-based rendering.
	for len(cells)%7 != 0 {
		tail := mustDate(cells[len(cells)-1].Date)
		cells = append(cells, heatCell{Date: tail.AddDate(0, 0, 1).Format("2006-01-02")})
	}
	return cells
}

// leveler maps a day's tokens onto a shade.
//
// Levels are quantiles of the days that actually have usage, not a fraction of
// the peak: daily usage spans several orders of magnitude, so any scale anchored
// to the maximum (linear or logarithmic) leaves most days sharing one shade.
// Quantiles spread the days evenly across the ramp, which is what makes the
// grid readable.
type leveler struct {
	// cuts holds the lower bound of levels 2..heatSteps, ascending.
	cuts []int64
}

func newLeveler(byDate map[string]usage.DateRow) leveler {
	var vals []int64
	for _, d := range byDate {
		if d.Tokens > 0 {
			vals = append(vals, d.Tokens)
		}
	}
	if len(vals) == 0 {
		return leveler{}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })

	cuts := make([]int64, 0, heatSteps-1)
	for i := 1; i < heatSteps; i++ {
		idx := i * len(vals) / heatSteps
		if idx >= len(vals) {
			idx = len(vals) - 1
		}
		cuts = append(cuts, vals[idx])
	}
	return leveler{cuts: cuts}
}

// level returns 0 for a day with no usage, otherwise 1..heatSteps.
func (l leveler) level(v int64) int {
	if v <= 0 {
		return 0
	}
	lv := 1
	for _, c := range l.cuts {
		if v >= c {
			lv++
		}
	}
	if lv > heatSteps {
		lv = heatSteps
	}
	return lv
}

func mustDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func sortedDates(m map[string]usage.DateRow) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func earliestDate(m map[string]usage.DateRow) time.Time {
	return mustDate(sortedDates(m)[0])
}

func latestDate(m map[string]usage.DateRow) time.Time {
	k := sortedDates(m)
	return mustDate(k[len(k)-1])
}

// startOfWeek returns the Sunday of the week containing t.
func startOfWeek(t time.Time) time.Time {
	return t.AddDate(0, 0, -int(t.Weekday()))
}

// Grid geometry. A cell is two columns of background colour with a one-column
// gap, so each day reads as a discrete square (a terminal cell is about twice
// as tall as it is wide). Every column is exactly colWidth wide, which is what
// keeps the month labels above their weeks.
const (
	cellWidth = 2
	gapWidth  = 1
	colWidth  = cellWidth + gapWidth
	labelWide = 4 // three-character weekday label plus a space
)

// renderHeatmap draws the calendar within width columns. A long history easily
// runs past a normal terminal, so when it does not fit only the most recent
// weeks are drawn -- a truncated row looks broken rather than merely long.
func renderHeatmap(cells []heatCell, width int) string {
	if len(cells) == 0 {
		return dimStyle.Render("  no data") + "\n"
	}

	maxWeeks := (width - labelWide) / colWidth
	if maxWeeks < 4 {
		maxWeeks = 4 // stay readable even on a very narrow terminal
	}

	totalWeeks := len(cells) / 7
	truncated := totalWeeks > maxWeeks
	if truncated {
		cells = cells[(totalWeeks-maxWeeks)*7:]
	}
	weeks := len(cells) / 7

	var b strings.Builder
	b.WriteString(monthRow(cells, weeks))
	b.WriteString("\n")
	for wd := 0; wd < 7; wd++ {
		b.WriteString(weekdayRow(cells, weeks, wd))
		b.WriteString("\n")
	}
	b.WriteString(heatFooter(cells, weeks, truncated, width))
	return b.String()
}

// monthRow labels the first week of each month, left-aligned on that week's
// column the way a calendar heading sits above its dates.
func monthRow(cells []heatCell, weeks int) string {
	row := strings.Repeat(" ", labelWide)
	lastMonth := ""
	for w := 0; w < weeks; w++ {
		label := "   "
		// Judge by the last day of the week: a week that only clips the tail of
		// a month should be labelled with the month that owns most of it.
		if t := mustDate(cells[w*7+6].Date); !t.IsZero() {
			if m := t.Format("Jan"); m != lastMonth {
				// Only label when the column has room before the next label.
				label = m
				lastMonth = m
			}
		}
		row += label
	}
	return dimStyle.Render(strings.TrimRight(row, " "))
}

// weekdayRow draws one weekday across all weeks. Only alternate days are
// labelled, as on GitHub: seven labels crowd the left edge and the row position
// already says which day it is.
func weekdayRow(cells []heatCell, weeks, wd int) string {
	labels := []string{"", "Mon", "", "Wed", "", "Fri", ""}

	var b strings.Builder
	b.WriteString(dimStyle.Render(pad(labels[wd], labelWide)))
	for w := 0; w < weeks; w++ {
		if w > 0 {
			b.WriteString(strings.Repeat(" ", gapWidth))
		}
		b.WriteString(heatCellStyle(cells[w*7+wd].Level))
	}
	return b.String()
}

// heatCellStyle renders one cell as a block of background colour, which is what
// makes a day read as a discrete square rather than a smudge of glyphs.
func heatCellStyle(level int) string {
	if lipgloss.ColorProfile() == termenv.Ascii {
		return strings.Repeat(heatShades[level], cellWidth)
	}
	return lipgloss.NewStyle().
		Background(heatLevels[level]).
		Render(strings.Repeat(" ", cellWidth))
}

// heatFooter is the legend plus a one-line caption: the range on show, the peak
// day, and whether history was cut to fit.
func heatFooter(cells []heatCell, weeks int, truncated bool, width int) string {
	var legend strings.Builder
	legend.WriteString(strings.Repeat(" ", labelWide))
	legend.WriteString(dimStyle.Render("less "))
	for i := 0; i <= heatSteps; i++ {
		if i > 0 {
			legend.WriteString(strings.Repeat(" ", gapWidth))
		}
		legend.WriteString(heatCellStyle(i))
	}
	legend.WriteString(dimStyle.Render(" more"))

	var peak heatCell
	for _, c := range cells {
		if c.Tokens > peak.Tokens {
			peak = c
		}
	}
	// Drop the optional parts rather than let the caption wrap, which would
	// break the block the grid sits in.
	parts := []string{fmt.Sprintf("%d weeks", weeks)}
	if truncated {
		parts = append(parts, "trimmed to fit")
	}
	if peak.Tokens > 0 {
		parts = append(parts, fmt.Sprintf("peak %s on %s", humanize(peak.Tokens), peak.Date))
	}
	caption := ""
	for i := range parts {
		if c := strings.Join(parts[:len(parts)-i], "  ·  "); labelWide+len(c) <= width {
			caption = c
			break
		}
	}

	return legend.String() + "\n" +
		strings.Repeat(" ", labelWide) + dimStyle.Render(caption) + "\n"
}

// pad right-pads s with spaces to width n.
func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
