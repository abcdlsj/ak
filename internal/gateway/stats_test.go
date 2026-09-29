package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

func TestMinuteSeriesSlide(t *testing.T) {
	var m minuteSeries
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	m.record(base)
	m.record(base.Add(30 * time.Second)) // same minute
	m.record(base.Add(time.Minute))
	if got := m.vals[statsBuckets-1]; got != 1 {
		t.Errorf("current bucket = %d, want 1", got)
	}
	if got := m.vals[statsBuckets-2]; got != 2 {
		t.Errorf("previous bucket = %d, want 2", got)
	}

	// A jump past the window resets it.
	m.record(base.Add(3 * time.Hour))
	for i := 0; i < statsBuckets-1; i++ {
		if m.vals[i] != 0 {
			t.Fatalf("bucket %d = %d after a reset, want 0", i, m.vals[i])
		}
	}
	if m.vals[statsBuckets-1] != 1 {
		t.Errorf("current bucket after a reset = %d, want 1", m.vals[statsBuckets-1])
	}
}

func TestSnapshotAtAlignsMembers(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	var a, b minuteSeries
	a.record(base)                  // 10:00
	b.record(base.Add(time.Minute)) // 10:01
	end := base.Add(time.Minute)

	av, bv := a.snapshotAt(end), b.snapshotAt(end)
	if av[statsBuckets-2] != 1 {
		t.Errorf("older member not at the second-to-last bucket: %v", av[statsBuckets-3:])
	}
	if bv[statsBuckets-1] != 1 {
		t.Errorf("newer member not at the last bucket: %v", bv[statsBuckets-3:])
	}
}

func TestStatsEndpoint(t *testing.T) {
	bad := newUpstream(t)
	bad.status = http.StatusServiceUnavailable
	good := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: bad.server.URL, APIKey: "sk-a"},
		"b": {Kind: config.KindClaude, BaseURL: good.server.URL, APIKey: "sk-b"},
	}, []string{"a", "b"})

	h := New(cfg).Handler()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var st Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	ps, ok := st.Pools["pool"]
	if !ok {
		t.Fatalf("no pool in stats: %s", rec.Body)
	}
	if len(ps.Members) != 2 {
		t.Fatalf("members = %+v", ps.Members)
	}
	// a failed then b served.
	if ps.Members[0].Fail != 1 || ps.Members[1].OK != 1 {
		t.Errorf("member stats = %+v", ps.Members)
	}
	if len(ps.Members[0].Series) != statsBuckets {
		t.Errorf("series length = %d, want %d", len(ps.Members[0].Series), statsBuckets)
	}
	var total int64
	for _, v := range ps.Members[0].Series {
		total += v
	}
	if total != 1 {
		t.Errorf("series total = %d, want 1", total)
	}
}
