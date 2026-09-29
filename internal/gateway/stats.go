package gateway

// Pool statistics: what each member served, minute by minute, so a client can
// show where a pool's traffic is going. The gateway keeps a short in-memory
// window; nothing is written to disk, so a restart starts the window over.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// statsBuckets is how many one-minute buckets the window holds (one hour).
const statsBuckets = 60

// statsBucket is the width of one bucket.
const statsBucket = time.Minute

// Stats is the gateway's current view, one entry per pool.
type Stats struct {
	BucketSecs int                  `json:"bucket_secs"`
	At         time.Time            `json:"at"`
	Pools      map[string]PoolStats `json:"pools"`
}

// PoolStats is one pool's members and their traffic.
type PoolStats struct {
	Name    string        `json:"name"`
	Members []MemberStats `json:"members"`
}

// MemberStats is one member's totals and its requests per minute over the
// window, oldest first.
type MemberStats struct {
	Name         string     `json:"name"`
	OK           int64      `json:"ok"`
	Fail         int64      `json:"fail"`
	Cooling      bool       `json:"cooling"`
	CoolingUntil *time.Time `json:"cooling_until,omitempty"`
	Series       []int64    `json:"series"`
}

// minuteSeries is a ring of one-minute request counts; vals[len-1] is the
// bucket starting at last.
type minuteSeries struct {
	last time.Time
	vals []int64
}

// record counts one request in the bucket now falls in, sliding the window
// when the minute has advanced.
func (m *minuteSeries) record(now time.Time) {
	b := now.Truncate(statsBucket)
	if m.vals == nil {
		m.vals = make([]int64, statsBuckets)
		m.last = b
	}
	gap := int(b.Sub(m.last) / statsBucket)
	switch {
	case gap >= statsBuckets:
		for i := range m.vals {
			m.vals[i] = 0
		}
		m.last = b
	case gap > 0:
		copy(m.vals, m.vals[gap:])
		for i := statsBuckets - gap; i < statsBuckets; i++ {
			m.vals[i] = 0
		}
		m.last = b
	}
	m.vals[len(m.vals)-1]++
}

// snapshotAt returns the window ending at end, so members recorded a minute
// apart still line up.
func (m *minuteSeries) snapshotAt(end time.Time) []int64 {
	out := make([]int64, statsBuckets)
	if m == nil || m.vals == nil {
		return out
	}
	shift := int(end.Sub(m.last) / statsBucket)
	for i := range m.vals {
		if j := i - shift; j >= 0 && j < statsBuckets {
			out[j] = m.vals[i]
		}
	}
	return out
}

// record counts a member's request. The caller holds the lock.
func (s *state) record(pool, member string, ok bool) {
	k := key(pool, member)
	if s.series[k] == nil {
		s.series[k] = &minuteSeries{}
	}
	s.series[k].record(time.Now())
	if ok {
		s.okN[k]++
	} else {
		s.failN[k]++
	}
}

// snapshot is the current stats for every pool in the config. The caller takes
// the lock.
func (s *state) snapshot(cfg *config.Config) Stats {
	now := time.Now()
	end := now.Truncate(statsBucket)
	out := Stats{BucketSecs: int(statsBucket.Seconds()), At: now, Pools: map[string]PoolStats{}}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if !p.IsPool() {
			continue
		}
		ps := PoolStats{Name: name}
		for _, m := range p.Members {
			k := key(name, m)
			ms := MemberStats{
				Name:   m,
				OK:     s.okN[k],
				Fail:   s.failN[k],
				Series: s.series[k].snapshotAt(end),
			}
			if until, ok := s.cool[k]; ok && until.After(now) {
				u := until
				ms.Cooling, ms.CoolingUntil = true, &u
			}
			ps.Members = append(ps.Members, ms)
		}
		out.Pools[name] = ps
	}
	return out
}

// handleStats serves GET /stats.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.st.mu.Lock()
	stats := s.st.snapshot(s.cfg)
	s.st.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// FetchStats reads a running gateway's stats. It is used by the TUI; the
// timeout keeps a missing gateway from delaying a frame.
func FetchStats(base string) (Stats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := strings.TrimRight(base, "/") + "/stats"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Stats{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Stats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Stats{}, &httpError{url: url, status: resp.Status}
	}
	var out Stats
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Stats{}, err
	}
	return out, nil
}

type httpError struct{ url, status string }

func (e *httpError) Error() string { return e.url + ": " + e.status }
