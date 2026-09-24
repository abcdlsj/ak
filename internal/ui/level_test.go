package ui

import (
	"fmt"
	"testing"

	"github.com/abcdlsj/ak/internal/usage"
)

// TestLevelDistribution 确认分级能区分不同量级。
// 真实用量跨度从几十万到几十亿,挤在同一级就失去意义。
func TestLevelDistribution(t *testing.T) {
	days := []usage.DateRow{
		{Date: "2026-09-20", Tokens: 100000},
		{Date: "2026-09-21", Tokens: 5000000},
		{Date: "2026-09-22", Tokens: 3937303481},
		{Date: "2026-09-23", Tokens: 1200000},
		{Date: "2026-09-24", Tokens: 800000000},
	}
	cells := buildHeatmap(days)
	for _, c := range cells {
		fmt.Printf("%s tokens=%-12d level=%2d\n", c.Date, c.Tokens, c.Level)
	}
	got := map[string]int{}
	for _, c := range cells {
		got[c.Date] = c.Level
	}
	if got["2026-09-22"] != 14 {
		t.Errorf("最大日 level=%d, 应为 14", got["2026-09-22"])
	}
	if got["2026-09-20"] == 0 {
		t.Error("最小日 level=0,应与无记录区分")
	}
	// 中间量级必须分散开
	lv := []int{got["2026-09-20"], got["2026-09-21"], got["2026-09-23"], got["2026-09-24"]}
	for i := 1; i < len(lv); i++ {
		if lv[i] == lv[i-1] && lv[i] != 14 {
			t.Errorf("不同量级挤在同一级: %v", lv)
			break
		}
	}
}
