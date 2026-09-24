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

// 热力图配色:低量用冷色,高量用暖色,一眼看出哪天花得多。
var heatLevels = []lipgloss.Color{
	lipgloss.Color("236"), // 0:无记录
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
	lipgloss.Color("202"), // 14:最高
}

// heatCell 是热力图的一格。
type heatCell struct {
	Date   string
	Tokens int64
	Cost   float64
	Level  int
}

// buildHeatmap 把按日期汇总转成 GitHub 风格的日历热力图:
// 每列一周,每行一周内的一天,从最早记录的周日开始。
func buildHeatmap(days []usage.DateRow) []heatCell {
	byDate := map[string]usage.DateRow{}
	for _, d := range days {
		byDate[d.Date] = d
	}
	if len(byDate) == 0 {
		return nil
	}

	// 找出最大 token 数用于分级。
	var max int64
	for _, d := range byDate {
		if d.Tokens > max {
			max = d.Tokens
		}
	}

	// 从最早日期的周日开始,到今天为止,填满每一天。
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
	// 补齐到整周,否则末尾几天在按周渲染时会越界丢失。
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
	// 用对数分级:用量分布跨度极大(几十万到几十亿),线性分级会让绝大多数格子挤在第 1 级。
	const steps = 14
	ratio := float64(v) / float64(max)
	lv := int(ratio10(ratio) * steps)
	// 非零用量至少 1 级,否则无法与无记录的日子区分。
	if lv < 1 {
		lv = 1
	}
	if lv > steps {
		lv = steps
	}
	return lv
}

// ratio10 把 (0,1] 的比值映射到 (0,1]。
//
// 用量跨度极大(几十万到几十亿,相差数万倍),必须对数分级,
// 否则绝大多数格子会挤在第 1 级,热力图看不出差异。
// floor 取 1e-3:低于最大值千分之一的视为最低一级,再低的都看不出来了。
func ratio10(ratio float64) float64 {
	if ratio >= 1 {
		return 1
	}
	const floor = 1e-4
	if ratio <= floor {
		return 0
	}
	// 把 [floor,1] 对数映射到 (0,1]。
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

// startOfWeek 返回所在周的周日。
func startOfWeek(t time.Time) time.Time {
	wd := int(t.Weekday()) // 0=周日
	return t.AddDate(0, 0, -wd)
}

// renderHeatmap 渲染热力图。每行是一周内的某一天,每列是一周。
func renderHeatmap(cells []heatCell) string {
	if len(cells) == 0 {
		return "  没有数据\n"
	}

	weekdayLabels := []string{"日", "一", "二", "三", "四", "五", "六"}
	var b strings.Builder

	// 行标签样式。
	labelStyle := lipgloss.NewStyle().Faint(true).Width(2)
	monthStyle := lipgloss.NewStyle().Faint(true)

	// 逐行渲染:7 行(周日到周六)。
	weeks := (len(cells) + 6) / 7
	for wd := 0; wd < 7; wd++ {
		b.WriteString(labelStyle.Render(weekdayLabels[wd]))
		b.WriteString(" ")

		// 月份标记行插在周三之前,和 GitHub 的做法一致。
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
			// 用 █ 而非空格:空格在部分终端背景上不可见,色块更直观。
			b.WriteString(style.Render("██"))
		}
		b.WriteString("\n")
	}

	// 图例。
	b.WriteString("\n  少 ")
	for i := 1; i <= 14; i += 3 {
		b.WriteString(lipgloss.NewStyle().Foreground(heatLevels[i]).Render("██"))
	}
	b.WriteString(" 多\n")
	return b.String()
}
