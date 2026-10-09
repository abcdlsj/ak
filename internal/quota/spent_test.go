package quota

import (
	"testing"
	"time"
)

func TestSpent(t *testing.T) {
	now := time.Now()
	t1, t2 := now.Add(time.Hour), now.Add(3*time.Hour)
	zero, some := 0.0, 5.0
	cases := []struct {
		name  string
		q     Quota
		spent bool
		back  *time.Time
	}{
		{"windows left", Quota{Windows: []Window{{Used: 40, Resets: &t1}}}, false, nil},
		{"5h spent", Quota{Windows: []Window{{Used: 100, Resets: &t1}, {Used: 60, Resets: &t2}}}, true, &t1},
		{"both spent, latest reset wins", Quota{Windows: []Window{{Used: 100, Resets: &t1}, {Used: 100, Resets: &t2}}}, true, &t2},
		{"spent with no reset", Quota{Windows: []Window{{Used: 100}}}, true, nil},
		{"balance gone", Quota{Balance: &zero}, true, nil},
		{"balance left", Quota{Balance: &some}, false, nil},
		{"failed query", Quota{Error: "x", Balance: &zero}, false, nil},
	}
	for _, c := range cases {
		spent, back := c.q.Spent()
		if spent != c.spent || (back == nil) != (c.back == nil) || (back != nil && !back.Equal(*c.back)) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, spent, back, c.spent, c.back)
		}
	}
	q := Quota{Windows: []Window{{Used: 100, Resets: &t1}, {Used: 50, Resets: &t2}, {Used: 10}}}
	if r := q.NextReset(); r == nil || !r.Equal(t2) {
		t.Errorf("NextReset = %v, want %v", r, t2)
	}
}
