package ui

import (
	"testing"

	"github.com/abcdlsj/ak/internal/usage"
)

// TestLevelDistribution confirms shading separates different magnitudes. Real
// daily usage runs from hundreds of thousands to billions, so crowding it into
// one shade would defeat the point of the grid.
func TestLevelDistribution(t *testing.T) {
	days := []usage.DateRow{
		{Date: "2026-09-20", Tokens: 100_000},
		{Date: "2026-09-21", Tokens: 5_000_000},
		{Date: "2026-09-22", Tokens: 3_937_303_481},
		{Date: "2026-09-23", Tokens: 1_200_000},
		{Date: "2026-09-24", Tokens: 800_000_000},
	}

	got := map[string]int{}
	for _, c := range buildHeatmap(days) {
		got[c.Date] = c.Level
	}

	if got["2026-09-22"] != heatSteps {
		t.Errorf("busiest day level=%d, want %d", got["2026-09-22"], heatSteps)
	}
	if got["2026-09-20"] == 0 {
		t.Error("quietest day with usage must not share level 0 with a day that has no record")
	}
	// Shading must rise with usage and actually use the ramp, otherwise the grid
	// says nothing about which days were heavy.
	seq := []string{"2026-09-20", "2026-09-23", "2026-09-21", "2026-09-24", "2026-09-22"}
	distinct := map[int]bool{}
	for i, d := range seq {
		distinct[got[d]] = true
		if i > 0 && got[d] < got[seq[i-1]] {
			t.Errorf("shading falls as usage rises: %s=%d, %s=%d",
				seq[i-1], got[seq[i-1]], d, got[d])
		}
	}
	if len(distinct) < heatSteps-1 {
		t.Errorf("five magnitudes crowded into %d shades", len(distinct))
	}
}

// TestLevelEmptyDay keeps "no record" visually distinct from "a little usage".
func TestLevelEmptyDay(t *testing.T) {
	days := []usage.DateRow{
		{Date: "2026-09-20", Tokens: 1},
		{Date: "2026-09-22", Tokens: 1_000_000},
	}
	for _, c := range buildHeatmap(days) {
		if c.Date == "2026-09-21" && c.Level != 0 {
			t.Errorf("gap day got level %d, want 0", c.Level)
		}
	}
}
