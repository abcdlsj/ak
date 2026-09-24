package ui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abcdlsj/ak/internal/usage"
)

// TestRenderHeatmap_FitsWidth 确认热力图不会超出给定宽度。
// 未做适配时,53 周的记录在 100 列终端下每行都被截断,画面残缺。
func TestRenderHeatmap_FitsWidth(t *testing.T) {
	var days []usage.DateRow
	// 400 天,约 57 周。
	base := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 0; i < 400; i++ {
		d := base.AddDate(0, 0, -i)
		days = append(days, usage.DateRow{
			Date:   d.Format("2006-01-02"),
			Tokens: int64(1000000 * (i + 1)),
		})
	}

	for _, width := range []int{80, 100, 120, 160, 200} {
		out := renderHeatmap(buildHeatmap(days), width)
		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			// 图例与提示行是短行,只检查含色块的网格行。
			if !strings.Contains(stripAnsiForTest(line), "█") {
				continue
			}
			if n := utf8.RuneCountInString(stripAnsiForTest(line)); n > width {
				t.Errorf("width=%d: line is %d columns, overflows", width, n)
			}
		}
	}
}

// TestRenderHeatmap_TruncationNoted 确认截断时给出提示,而不是静默丢历史。
func TestRenderHeatmap_TruncationNoted(t *testing.T) {
	var days []usage.DateRow
	base := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 0; i < 400; i++ {
		d := base.AddDate(0, 0, -i)
		days = append(days, usage.DateRow{Date: d.Format("2006-01-02"), Tokens: int64(i + 1)})
	}

	narrow := renderHeatmap(buildHeatmap(days), 80)
	if !strings.Contains(narrow, "only the last") {
		t.Error("narrow terminal should note that history is truncated")
	}

	wide := renderHeatmap(buildHeatmap(days), 400)
	if strings.Contains(wide, "only the last") {
		t.Error("wide terminal should not claim truncation")
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
