package ui

import (
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/usage"
)

// A short window keeps 5-minute points; a long one merges them into a
// round interval without losing tokens.
func TestBinTimeline(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pts := []usage.Point{
		{Time: from.Add(10 * time.Minute), Tokens: 3},
		{Time: from.Add(12 * time.Hour), Tokens: 4},
		{Time: from.Add(-time.Minute), Tokens: 100}, // before the window
	}
	vals, step := binTimeline(pts, from, from.Add(24*time.Hour), 400)
	if step != 5*time.Minute || len(vals) != 288 || vals[2] != 3 || vals[144] != 4 {
		t.Fatalf("step = %v, len = %d", step, len(vals))
	}
	vals, step = binTimeline(pts, from, from.Add(7*24*time.Hour), 100)
	var sum int64
	for _, v := range vals {
		sum += v
	}
	if step != 2*time.Hour || sum != 7 || stepLabel(step) != "2h" {
		t.Fatalf("step = %v, sum = %d", step, sum)
	}
}
