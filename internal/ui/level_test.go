package ui

import (
	"fmt"
	"testing"

	"github.com/abcdlsj/ak/internal/usage"
)

// TestLevelDistribution confirms leveling can distinguish different magnitudes.
// Real usage spans from hundreds of thousands to billions, so crowding
// everything into one level would defeat the purpose.
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
		t.Errorf("max day level=%d, want 14", got["2026-09-22"])
	}
	if got["2026-09-20"] == 0 {
		t.Error("min day level=0, should differ from a day with no record")
	}
	// The middle magnitudes must be spread apart.
	lv := []int{got["2026-09-20"], got["2026-09-21"], got["2026-09-23"], got["2026-09-24"]}
	for i := 1; i < len(lv); i++ {
		if lv[i] == lv[i-1] && lv[i] != 14 {
			t.Errorf("different magnitudes crowded into one level: %v", lv)
			break
		}
	}
}
