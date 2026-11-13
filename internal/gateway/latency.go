package gateway

import (
	"sort"
	"sync"
	"time"
)

// latencyKeep is how many recent requests a route's figures come from: enough
// to see a p95 move, few enough to stay honest when a vendor slows down.
const latencyKeep = 256

// LatencyRow is one route's recent timings. First is what the agent waited
// for the first byte of its answer, Total the whole request.
type LatencyRow struct {
	Route    string
	Requests int
	First50  time.Duration
	First95  time.Duration
	Total50  time.Duration
	Total95  time.Duration
}

// latencies keeps each route's last latencyKeep requests, in two rings.
type latencies struct {
	mu sync.Mutex
	m  map[string]*latencyRing
}

type latencyRing struct {
	first   []time.Duration
	total   []time.Duration
	next    int
	samples int
}

func newLatencies() *latencies { return &latencies{m: map[string]*latencyRing{}} }

func (l *latencies) add(route string, first, total time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[route]
	if r == nil {
		r = &latencyRing{
			first: make([]time.Duration, latencyKeep),
			total: make([]time.Duration, latencyKeep),
		}
		l.m[route] = r
	}
	r.first[r.next], r.total[r.next] = first, total
	r.next = (r.next + 1) % latencyKeep
	r.samples = min(r.samples+1, latencyKeep)
}

// rows is every route's figures, sorted by name.
func (l *latencies) rows() []LatencyRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LatencyRow, 0, len(l.m))
	for name, r := range l.m {
		first := append([]time.Duration(nil), r.first[:r.samples]...)
		total := append([]time.Duration(nil), r.total[:r.samples]...)
		out = append(out, LatencyRow{
			Route: name, Requests: r.samples,
			First50: percentile(first, 50), First95: percentile(first, 95),
			Total50: percentile(total, 50), Total95: percentile(total, 95),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Route < out[j].Route })
	return out
}

// Latencies is the service's recent timings per route, for clients and the
// console.
func (s *Server) Latencies() []LatencyRow { return s.lat.rows() }

// percentile is the nearest-rank p-th percentile of ds, which it sorts.
func percentile(ds []time.Duration, p int) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	i := (p*len(ds)+99)/100 - 1
	return ds[max(min(i, len(ds)-1), 0)]
}
