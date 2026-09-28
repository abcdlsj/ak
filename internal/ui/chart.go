package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/usage"
	"github.com/charmbracelet/lipgloss"
)

// binTimeline sums points into at most n bins covering [from, to). A bin is a
// whole number of timeline slots, so each point stays an exact 5-minute sum
// when the window is short enough.
func binTimeline(pts []usage.Point, from, to time.Time, n int) ([]int64, time.Duration) {
	slots := int((to.Sub(from) + usage.SlotLen - 1) / usage.SlotLen)
	if slots < 1 || n < 1 {
		return nil, usage.SlotLen
	}
	step := niceStep(time.Duration((slots+n-1)/n) * usage.SlotLen)
	per := int(step / usage.SlotLen)
	vals := make([]int64, (slots+per-1)/per)
	for _, p := range pts {
		if i := int(p.Time.Sub(from) / step); p.Time.Compare(from) >= 0 && i < len(vals) {
			vals[i] += p.Tokens
		}
	}
	return vals, step
}

// niceSteps are the bin widths a chart may use, so the heading reads as a
// round interval.
var niceSteps = []time.Duration{
	5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour,
}

// niceStep rounds d up to the next nice width, then to whole weeks.
func niceStep(d time.Duration) time.Duration {
	for _, s := range niceSteps {
		if d <= s {
			return s
		}
	}
	week := niceSteps[len(niceSteps)-1]
	return (d + week - 1) / week * week
}

// brailleDots maps a dot's column (0-1) and row from the top (0-3) within a
// cell to its bit in the braille block.
var brailleDots = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// lineChart draws vals as a braille line, two points per column and four
// levels per row, with the peak and zero marked on the left.
func lineChart(vals []int64, height int) string {
	var peak int64
	for _, v := range vals {
		peak = max(peak, v)
	}
	cols := (len(vals) + 1) / 2
	if peak == 0 || cols == 0 || height < 1 {
		return ""
	}
	grid := make([][]rune, height)
	for r := range grid {
		grid[r] = make([]rune, cols)
	}
	dots := height * 4
	set := func(x, y int) {
		grid[height-1-y/4][x/2] |= brailleDots[x%2][3-y%4]
	}
	prev := -1
	for x, v := range vals {
		y := int(float64(v) / float64(peak) * float64(dots-1))
		lo, hi := y, y
		// Join to the previous point so steep changes stay one line.
		if prev >= 0 {
			lo, hi = min(y, prev), max(y, prev)
		}
		for d := lo; d <= hi; d++ {
			set(x, d)
		}
		prev = y
	}

	labelW := max(len(humanize(peak)), 1)
	lines := make([]string, height)
	for r, cells := range grid {
		label := ""
		switch r {
		case 0:
			label = humanize(peak)
		case height - 1:
			label = "0"
		}
		var b strings.Builder
		for _, c := range cells {
			if c == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(0x2800 + c)
			}
		}
		lines[r] = dimStyle.Render(fmt.Sprintf("%*s │", labelW, label)) + barStyle.Render(b.String())
	}
	return strings.Join(lines, "\n")
}

// timelineAxis labels the chart's two ends under the plot.
func timelineAxis(from, to time.Time, indent, width int) string {
	layout := "01-02"
	if to.Sub(from) <= 48*time.Hour {
		layout = "01-02 15:04"
	}
	l, r := from.Format(layout), to.Format(layout)
	gap := max(width-len(l)-len(r), 1)
	return dimStyle.Render(strings.Repeat(" ", indent) + l + strings.Repeat(" ", gap) + r)
}

// stepLabel names a nice bin width: 5m, 2h, 1d, 1w.
func stepLabel(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%(7*24*time.Hour) == 0:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// timelineChart is the drill-down's tokens-over-time section for the window
// starting on since (all history when empty), height rows tall.
func timelineChart(pts []usage.Point, since string, width, height int) string {
	if len(pts) == 0 {
		return ""
	}
	from := pts[0].Time
	if since != "" {
		if t, err := time.ParseInLocation("2006-01-02", since, time.Local); err == nil {
			from = t
		}
	}
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	to := time.Now().Truncate(usage.SlotLen).Add(usage.SlotLen)

	// The label column is at most as wide as the widest humanized number.
	indent := len(humanize(999_999_999)) + 2
	plotW := max(width-indent, 8)
	vals, step := binTimeline(pts, from, to, plotW*2)
	chart := lineChart(vals, height)
	if chart == "" {
		return ""
	}
	chartIndent := lipgloss.Width(strings.SplitN(chart, "\n", 2)[0]) - (len(vals)+1)/2
	return section("Tokens per "+stepLabel(step), width) + "\n" +
		chart + "\n" + timelineAxis(from, to, chartIndent, (len(vals)+1)/2)
}
