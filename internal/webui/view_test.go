package webui

import (
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/gateway"
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

// The calendar is the last n days, padded to the first day's weekday, with
// no usage a shade of its own and the peak named.
func TestHeatCells(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)
	rows := []gateway.UsageRow{
		{Day: "2026-11-12", Input: 900, Output: 100},
		{Day: "2026-11-10", Input: 10},
	}
	cells, peak := heatCells(rows, 7, now)
	lead := int(now.AddDate(0, 0, -6).Weekday())
	if len(cells) != 7+lead {
		t.Fatalf("%d cells, want %d (7 days and %d padding)", len(cells), 7+lead, lead)
	}
	if cells[0].Level != -1 {
		t.Errorf("the padding is visible: %+v", cells[0])
	}
	last := cells[len(cells)-1]
	if last.Date != "2026-11-12" || last.Level == 0 {
		t.Errorf("today: %+v", last)
	}
	if peak != "1k on 11-12" {
		t.Errorf("peak = %q", peak)
	}
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
