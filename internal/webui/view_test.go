package webui

import (
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/quota"
)

// TestPix covers the pixel font: lit pixels become rectangles, a rune it
// lacks is skipped, and nothing at all renders nothing.
func TestPix(t *testing.T) {
	one := string(pix("1"))
	if !strings.HasPrefix(one, `<svg class="pix" viewBox="0 0 3 5"`) {
		t.Errorf("one glyph: %s", one)
	}
	if n := strings.Count(one, "<rect"); n != 5 {
		t.Errorf("one has %d lit runs, want 5: %s", n, one)
	}
	if two := string(pix("12")); !strings.Contains(two, `viewBox="0 0 7 5"`) {
		t.Errorf("two glyphs, a column apart: %s", two)
	}
	if got := pix("x"); got != "" {
		t.Errorf("a rune the font lacks: %q", got)
	}
}

func TestResetText(t *testing.T) {
	now := time.Now()
	in := func(d time.Duration) *time.Time {
		t := now.Add(d)
		return &t
	}
	day := 24 * time.Hour
	for _, tc := range []struct {
		when *time.Time
		want string
	}{
		{nil, ""},
		{in(-time.Minute), "↻ resetting"},
		{in(90 * time.Minute), "↻ in 1h 30m"},
		{in(3 * day), "↻ " + now.Add(3*day).Local().Format("01-02 15:04")},
	} {
		if got := resetText(tc.when); got != tc.want {
			t.Errorf("resetText = %q, want %q", got, tc.want)
		}
	}
}

func TestNumbers(t *testing.T) {
	for _, tc := range []struct {
		in  int64
		sep string // number, grouped by thin spaces
		few string // humanize, three significant figures
	}{
		{0, "0", "0"},
		{999, "999", "999"},
		{1500, "1 500", "1.5k"},
		{2_300_000, "2 300 000", "2.3M"},
		{1_000_000_000, "1 000 000 000", "1G"},
	} {
		if got := number(tc.in); got != tc.sep {
			t.Errorf("number(%d) = %q, want %q", tc.in, got, tc.sep)
		}
		if got := humanize(tc.in); got != tc.few {
			t.Errorf("humanize(%d) = %q, want %q", tc.in, got, tc.few)
		}
	}
}

// A card's hue comes from its key and stays in the palette's range.
func TestHueOf(t *testing.T) {
	seen := map[int]bool{}
	for _, k := range []string{"bili", "command-code", "opendesign", "kimi"} {
		h := hueOf(k)
		if h < 0 || h >= hues {
			t.Fatalf("hueOf(%q) = %d", k, h)
		}
		if h != hueOf(k) {
			t.Errorf("hueOf(%q) is not stable", k)
		}
		seen[h] = true
	}
	if len(seen) < 2 {
		t.Errorf("four vendors share one hue")
	}
}

// The calendar is GitHub's year: whole weeks from the Sunday of the week a
// year ago, the months above their columns, and the total split for the pixel
// font.
func TestHeatYear(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)
	rows := []gateway.UsageRow{
		{Day: "2026-11-12", Input: 900, Output: 100},
		{Day: "2026-11-10", Input: 10},
	}
	h := buildHeatYear(rows, now)
	if len(h.Cells)%7 != 0 {
		t.Fatalf("%d cells, not whole weeks", len(h.Cells))
	}
	if got := len(h.Cells) / 7; got != h.Cols {
		t.Errorf("Cols = %d, but %d columns of cells", h.Cols, got)
	}
	if h.Cols < 52 || h.Cols > 54 {
		t.Errorf("%d weeks a year, want 52 to 54", h.Cols)
	}
	start := mustDate(t, h.Cells[0].Date)
	if start.Weekday() != time.Sunday {
		t.Errorf("the first cell is a %v, not a Sunday", start.Weekday())
	}
	today := -1
	for i, c := range h.Cells {
		if c.Date == "2026-11-12" {
			today = i
		}
	}
	if today < 0 || today%7 != int(now.Weekday()) {
		t.Errorf("today is at %d, not in its weekday row", today)
	}
	if last := h.Cells[len(h.Cells)-1]; last.Level != -1 {
		t.Errorf("the last week is not padded: %+v", last)
	}
	if h.Total != "1" || h.Unit != "k" {
		t.Errorf("total = %q %q, want 1 k", h.Total, h.Unit)
	}
	if h.Peak != "1k on 11-12" {
		t.Errorf("peak = %q", h.Peak)
	}
	// A month is named once, above the week it starts in.
	named := map[string]bool{}
	prev := 0
	for _, m := range h.Months {
		if named[m.Label] {
			t.Errorf("%s is named twice", m.Label)
		}
		named[m.Label] = true
		if m.Col <= prev {
			t.Errorf("months out of order: %+v", h.Months)
		}
		prev = m.Col
	}
	if len(h.Months) < 10 {
		t.Errorf("%d months named, want most of a year", len(h.Months))
	}
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		t.Fatal("a padded cell was taken for a date")
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestBarWidth(t *testing.T) {
	for _, tc := range []struct{ v, most, want int }{
		{0, 10, 0}, {10, 10, 100}, {5, 10, 50}, {1, 1000, 2}, {20, 10, 100},
	} {
		if got := barWidth(int64(tc.v), int64(tc.most)); got != tc.want {
			t.Errorf("barWidth(%d, %d) = %d, want %d", tc.v, tc.most, got, tc.want)
		}
	}
}

func TestMetersLeft(t *testing.T) {
	ms := meters(quota.Quota{Windows: []quota.Window{
		{Name: "5h", Used: 25},
		{Name: "weekly", Used: 85},
		{Name: "monthly", Used: 100},
	}})
	if len(ms) != 3 {
		t.Fatalf("len(meters) = %d, want 3", len(ms))
	}
	if ms[0].Pct != 75 || ms[0].Fill != 75 || ms[0].Level != "" {
		t.Errorf("ms[0] = %+v, want 75%% left, level empty", ms[0])
	}
	if ms[1].Pct != 15 || ms[1].Fill != 15 || ms[1].Level != "high" {
		t.Errorf("ms[1] = %+v, want 15%% left, level high", ms[1])
	}
	if ms[2].Pct != 0 || ms[2].Fill != 0 || ms[2].Level != "spent" {
		t.Errorf("ms[2] = %+v, want 0%% left, level spent", ms[2])
	}
}
