package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// sparkGlyphs are the eight levels of a block sparkline, lowest first.
var sparkGlyphs = []rune("▁▂▃▄▅▆▇█")

// sparkline draws vals as block characters scaled to peak, keeping at most
// width points (the newest). A zero is blank, so idle minutes read as gaps.
func sparkline(vals []int64, peak int64, width int) string {
	if len(vals) == 0 || width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	if peak <= 0 {
		peak = 1
	}
	var b strings.Builder
	for _, v := range vals {
		if v <= 0 {
			b.WriteByte(' ')
			continue
		}
		level := int(float64(v)/float64(peak)*float64(len(sparkGlyphs)-1) + 0.5)
		b.WriteRune(sparkGlyphs[clamp(level, 0, len(sparkGlyphs)-1)])
	}
	return b.String()
}

// poolFlow renders where a pool's requests are going: one sparkline per member
// over the gateway's window, plus the pool's own total and each member's
// totals, scaled together so the lines are comparable. It is the manage page's
// flow section for a pool.
func poolFlow(a *app, name string, width int) string {
	if width < 24 {
		return ""
	}
	ps, known := a.poolStats[name]
	if !known {
		hint := "waiting for the gateway…"
		if a.poolStatsErr != nil {
			hint = "gateway not running · run `ak serve`"
		}
		return section("flow", width) + "\n" + dimStyle.Render("  "+truncate(hint, width-2))
	}

	n := 0
	for _, m := range ps.Members {
		n = max(n, len(m.Series))
	}
	if n == 0 {
		return section("flow", width) + "\n" + dimStyle.Render("  no traffic yet")
	}

	// One scale for every line, so a busier member visibly outranks a quieter
	// one instead of each line being normalized on its own.
	total := make([]int64, n)
	var peak int64
	for _, m := range ps.Members {
		for i, v := range m.Series {
			total[i] += v
			peak = max(peak, v)
		}
	}
	for _, v := range total {
		peak = max(peak, v)
	}

	labelW := len("pool")
	for _, m := range ps.Members {
		labelW = max(labelW, lipgloss.Width(m.Name))
	}
	const tailW = 24
	plotW := max(width-labelW-tailW-4, 8)

	var b strings.Builder
	b.WriteString(section(fmt.Sprintf("flow · req/min · last %dm", n), width))
	b.WriteString("\n")
	row := func(label string, series []int64, tail string, style lipgloss.Style) {
		b.WriteString(style.Render(fmt.Sprintf("  %-*s ", labelW, label)))
		b.WriteString(barStyle.Render(sparkline(series, peak, plotW)))
		b.WriteString("  " + dimStyle.Render(truncate(tail, tailW)))
		b.WriteString("\n")
	}

	var sum int64
	for _, v := range total {
		sum += v
	}
	row("pool", total, fmt.Sprintf("%d req", sum), rowStyle)
	for _, m := range ps.Members {
		var tail strings.Builder
		fmt.Fprintf(&tail, "ok %d", m.OK)
		if m.Fail > 0 {
			fmt.Fprintf(&tail, " fail %d", m.Fail)
		}
		style := rowStyle
		if m.Cooling {
			style = warnStyle
			if m.CoolingUntil != nil {
				if d := time.Until(*m.CoolingUntil); d > 0 {
					fmt.Fprintf(&tail, " cooling %s", d.Truncate(time.Second))
				} else {
					tail.WriteString(" cooling")
				}
			} else {
				tail.WriteString(" cooling")
			}
		}
		row(m.Name, m.Series, tail.String(), style)
	}
	return strings.TrimRight(b.String(), "\n")
}
