package ui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abcdlsj/ak/internal/usage"
)

func syntheticDays(n int, tokens func(i int) int64) []usage.DateRow {
	var days []usage.DateRow
	base := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 0; i < n; i++ {
		d := base.AddDate(0, 0, -i)
		days = append(days, usage.DateRow{Date: d.Format("2006-01-02"), Tokens: tokens(i)})
	}
	return days
}

// TestRenderHeatmap_FitsWidth confirms the grid never overflows the width it is
// given. Without fitting, 57 weeks of history wraps on an 80-column terminal and
// the whole picture falls apart.
func TestRenderHeatmap_FitsWidth(t *testing.T) {
	days := syntheticDays(400, func(i int) int64 { return int64(1_000_000 * (i + 1)) })

	for _, width := range []int{40, 80, 100, 120, 160, 200} {
		out := renderHeatmap(buildHeatmap(days), width)
		for _, line := range strings.Split(out, "\n") {
			if n := utf8.RuneCountInString(stripAnsiForTest(line)); n > width {
				t.Errorf("width=%d: line is %d columns, overflows: %q", width, n, stripAnsiForTest(line))
			}
		}
	}
}

// TestRenderHeatmap_TruncationNoted confirms trimming history to fit is stated,
// not silent.
func TestRenderHeatmap_TruncationNoted(t *testing.T) {
	days := syntheticDays(400, func(i int) int64 { return int64(i + 1) })

	narrow := renderHeatmap(buildHeatmap(days), 80)
	if !strings.Contains(narrow, "trimmed to fit") {
		t.Error("narrow terminal should note that history is trimmed")
	}

	wide := renderHeatmap(buildHeatmap(days), 400)
	if strings.Contains(wide, "trimmed to fit") {
		t.Error("wide terminal should not claim trimming")
	}
}

// TestRenderHeatmap_GridShape confirms the grid is seven weekday rows of equal
// width, which is what keeps the columns square and the month labels aligned.
func TestRenderHeatmap_GridShape(t *testing.T) {
	days := syntheticDays(90, func(i int) int64 { return int64(1_000 * (i + 1)) })
	out := renderHeatmap(buildHeatmap(days), 100)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// month row + 7 weekday rows + legend + caption
	if len(lines) != 10 {
		t.Fatalf("got %d lines, want 10:\n%s", len(lines), out)
	}
	want := utf8.RuneCountInString(stripAnsiForTest(lines[1]))
	for _, l := range lines[1:8] {
		if n := utf8.RuneCountInString(stripAnsiForTest(l)); n != want {
			t.Errorf("weekday rows differ in width: %d vs %d", n, want)
		}
	}
}

func stripAnsiForTest(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
