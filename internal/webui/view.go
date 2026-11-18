package webui

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/quota"
)

// The web UI keeps the TUI board's look: a hue per card, numbers in the same
// pixel font, tick bars, and amber or red once an allowance runs out. The
// helpers here are the parts of that the pages share.

// hues are the card colours, in the order style.css defines them; a card
// keeps its own across reloads, as on the board.
var hues = 6

// hueOf picks a card's colour from its key.
func hueOf(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % uint32(hues))
}

// glyphs is a 3-wide, 5-tall font for the numbers the board draws big. It is
// the same table as the TUI's, so a number looks the same in both.
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
	'-': {" ", " ", "###", " ", " "},
}

// pix draws s in the pixel font as an inline SVG: one rectangle per lit
// pixel, at the size CSS gives the glyphs. A rune the font lacks is left out.
func pix(s string) template.HTML {
	var rects strings.Builder
	width, first := 0, true
	for _, r := range s {
		g, ok := glyphs[r]
		if !ok {
			continue
		}
		if !first {
			width++
		}
		first = false
		for y, row := range g {
			for x := 0; x < len(row); {
				if row[x] != '#' {
					x++
					continue
				}
				run := 1
				for x+run < len(row) && row[x+run] == '#' {
					run++
				}
				fmt.Fprintf(&rects, `<rect x="%d" y="%d" width="%d" height="1"/>`,
					width+x, y, run)
				x += run
			}
		}
		width += len(g[0])
	}
	if width == 0 {
		return ""
	}
	return template.HTML(fmt.Sprintf(
		`<svg class="pix" viewBox="0 0 %d 5" aria-hidden="true" focusable="false">%s</svg>`,
		width, rects.String()))
}

// resetText says when an allowance comes back: in 2h 14m, or at 01-02 15:04
// when that is days away.
func resetText(t *time.Time) string {
	if t == nil {
		return ""
	}
	d := time.Until(*t)
	switch {
	case d <= 0:
		return "↻ resetting"
	case d < 48*time.Hour:
		return "↻ in " + short(d)
	}
	return "↻ " + t.Local().Format("01-02 15:04")
}

// short is a duration to the minute: 2h 14m, 5m, 3d 4h.
func short(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0 && mins == 0:
		return fmt.Sprintf("%dh", hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", max(mins, 1))
}

// money is an amount as the board writes it.
func money(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// humanize is a count to three significant figures: 12 345, 3.2M.
func humanize(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return trimZero(float64(n)/1e3) + "k"
	case n < 1_000_000_000:
		return trimZero(float64(n)/1e6) + "M"
	}
	return trimZero(float64(n)/1e9) + "G"
}

// size splits a count into the digits a pixel number draws and the unit
// after them: the font has no letters.
func size(n int64) (string, string) {
	switch {
	case n < 1_000:
		return strconv.FormatInt(n, 10), ""
	case n < 1_000_000:
		return trimZero(float64(n) / 1e3), "k"
	case n < 1_000_000_000:
		return trimZero(float64(n) / 1e6), "M"
	}
	return trimZero(float64(n) / 1e9), "G"
}

func trimZero(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// number prints a count with thin grouping: 12 345.
func number(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// meterView is one allowance on a card: its name and reset, how much is
// used, and how wide its tick bar fills.
type meterView struct {
	Name  string
	Pct   int
	Fill  int // Pct, kept to the bar's width
	Reset string
	Level string // "", "high" or "spent", as the board colours it
}

// amountView is money rather than a share: a balance, in the same pixel font.
type amountView struct {
	Name, Note, Value, Label string
}

// tileView is one headline number above a page's tables: the digits the
// pixel font draws and, for a count too big to draw, the unit after them.
type tileView struct {
	Label, Value, Unit string
}

// tile is a headline number. A count is drawn whole; a token count is
// shortened to three figures and carries its unit.
func tile(label string, n int64, _ string) tileView {
	if n < 10_000 {
		return tileView{Label: label, Value: strconv.FormatInt(n, 10)}
	}
	num, unit := size(n)
	return tileView{Label: label, Value: num, Unit: unit}
}

// meters turns a reading's windows into the card's rows.
func meters(q quota.Quota) []meterView {
	out := make([]meterView, 0, len(q.Windows))
	for _, w := range q.Windows {
		pct := int(w.Left() + 0.5)
		out = append(out, meterView{Name: w.Name, Pct: pct, Fill: min(pct, 100),
			Reset: resetText(w.Resets), Level: levelName(w.Used)})
	}
	return out
}

// levelName is where a share stands: the card's hue, amber near the end, red
// once spent.
func levelName(used float64) string {
	switch {
	case used >= 100:
		return "spent"
	case used >= 80:
		return "high"
	}
	return ""
}

// heatMonth is a month's name above the week it starts, one-based column.
type heatMonth struct {
	Label string
	Col   int
}

// heatYear is the last twelve months of usage as GitHub draws its year: one
// column a week, one row a weekday, from the Sunday of the week a year ago to
// today, the months above the columns they start. Shades are quantiles of the
// days with usage, so a few busy days do not flatten the rest into one.
type heatYear struct {
	Cells  []heatCell
	Months []heatMonth
	Cols   int
	Total  string // the digits to draw big, and
	Unit   string // what they count in
	Peak   string
}

func buildHeatYear(rows []gateway.UsageRow, now time.Time) heatYear {
	per := map[string]int64{}
	for _, r := range rows {
		per[r.Day] += r.Input + r.Output + r.CacheRead + r.CacheWrite
	}
	var vals []int64
	for _, v := range per {
		if v > 0 {
			vals = append(vals, v)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	cuts := make([]int64, 0, heatSteps-1)
	for i := 1; i < heatSteps && len(vals) > 0; i++ {
		cuts = append(cuts, vals[min(i*len(vals)/heatSteps, len(vals)-1)])
	}
	level := func(v int64) int {
		if v <= 0 {
			return 0
		}
		lv := 1
		for _, c := range cuts {
			if v >= c {
				lv++
			}
		}
		return min(lv, heatSteps)
	}

	// Start on the Sunday of the week a year ago, so a column is a week.
	start := now.AddDate(0, 0, -364)
	start = start.AddDate(0, 0, -int(start.Weekday()))
	column := func(d time.Time) int { return int(d.Sub(start).Hours()/24) / 7 }

	var out []heatCell
	var total, peak int64
	var peakDay string
	for d := start; !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		v := per[key]
		total += v
		if v > peak {
			peak, peakDay = v, d.Format("01-02")
		}
		out = append(out, heatCell{Date: key, Level: level(v),
			Label: d.Format("01-02") + ": " + humanize(v) + " tokens"})
	}
	// Complete the last week, so its column is not short.
	for len(out)%7 != 0 {
		out = append(out, heatCell{Level: -1})
	}

	// A month is named above the week it starts in; one that would run into
	// the name before it is left out, since the next says enough.
	var months []heatMonth
	named := map[string]bool{}
	for d := start; !d.After(now); d = d.AddDate(0, 0, 1) {
		m := d.Format("Jan")
		if named[m] || d.Day() > 7 {
			continue
		}
		named[m] = true
		col := column(d) + 1
		if n := len(months); n > 0 && col-months[n-1].Col < 3 {
			continue
		}
		months = append(months, heatMonth{Label: m, Col: col})
	}

	num, unit := size(total)
	hy := heatYear{Cells: out, Months: months, Cols: len(out) / 7, Total: num, Unit: unit}
	if peak > 0 {
		hy.Peak = humanize(peak) + " on " + peakDay
	}
	return hy
}

// heatSteps is the number of shades above "no record".
const heatSteps = 4

// usageYear is how far back the calendar reads: the year the service keeps.
const usageYear = 366

// heatCell is one day of the calendar.
type heatCell struct {
	Date   string
	Label  string
	Tokens string
	Level  int
}

// usageTotals sums a range's rows, for the tiles above the tables.
type usageTotals struct {
	Requests int64
	Failed   int64
	Input    int64
	Output   int64
	Cache    int64
}

func totalsOf(rows []gateway.UsageRow) usageTotals {
	var t usageTotals
	for _, r := range rows {
		t.Requests += r.Requests
		t.Failed += r.Failed
		t.Input += r.Input
		t.Output += r.Output
		t.Cache += r.CacheRead + r.CacheWrite
	}
	return t
}

// activityDays is the window a card's activity line counts over.
const activityDays = 30

// activity is what a token, a route or an account asked for lately: the
// requests in the window and the day it was last asked.
type activity struct {
	Requests int64
	Last     string
}

// activities sums a window's rows by whatever pick names, as the cards show
// them under a card's own numbers.
func activities(rows []gateway.UsageRow, pick func(gateway.UsageRow) string) map[string]activity {
	out := map[string]activity{}
	for _, r := range rows {
		k := pick(r)
		if k == "" {
			continue
		}
		a := out[k]
		a.Requests += r.Requests
		if r.Day > a.Last {
			a.Last = r.Day[5:]
		}
		out[k] = a
	}
	return out
}

// latencyView is one route's recent timings as a page shows them.
type latencyView struct {
	Route            string
	Requests         string
	First50, First95 string
	Total50, Total95 string
}

// latencyRows turns the service's timings into the Usage page's table.
func latencyRows(rows []gateway.LatencyRow) []latencyView {
	out := make([]latencyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, latencyView{Route: r.Route, Requests: number(int64(r.Requests)),
			First50: shortTime(r.First50), First95: shortTime(r.First95),
			Total50: shortTime(r.Total50), Total95: shortTime(r.Total95)})
	}
	return out
}

// shortTime writes a duration the way an operator reads it: 420ms, 3.2s.
func shortTime(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// barWidth is how full a row's bar is, against the largest row.
func barWidth(v, most int64) int {
	if most <= 0 || v <= 0 {
		return 0
	}
	n := int(float64(v) / float64(most) * 100)
	return max(min(n, 100), 2)
}

// maxRequests is the largest request count in a table, which its bars share.
func maxRequests(groups []usageGroup) int64 {
	var m int64
	for _, g := range groups {
		m = max(m, g.Requests)
	}
	return m
}
