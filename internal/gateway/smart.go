package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
)

// quotaEvery is how often the gateway reads the quota of smart pools' members.
var quotaEvery = 5 * time.Minute

// allowance is what the last quota reading said of a provider.
type allowance struct {
	known bool
	spent bool
	back  *time.Time // when a spent provider has allowance again
	next  *time.Time // the soonest reset among windows with allowance left
}

func allowanceOf(q quota.Quota) allowance {
	if q.Error != "" {
		return allowance{}
	}
	spent, back := q.Spent()
	return allowance{known: true, spent: spent, back: back, next: q.NextReset()}
}

// Classes of smart ranking, best first.
const (
	rankResets  = iota // allowance left, and a window that resets: use it before it does
	rankUnknown        // allowance left with no reset (a balance), or not read yet
	rankSpent          // nothing left until back
)

// smartRank orders members for the smart strategy: allowance that resets
// soonest first, so the least of it is lost at the reset; then members with
// no reset to race; then spent ones by when they come back.
type smartRank struct {
	class int
	at    time.Time
}

func (a allowance) rank() smartRank {
	switch {
	case !a.known:
		return smartRank{class: rankUnknown}
	case a.spent:
		r := smartRank{class: rankSpent}
		if a.back != nil {
			r.at = *a.back
		}
		return r
	case a.next != nil:
		return smartRank{class: rankResets, at: *a.next}
	}
	return smartRank{class: rankUnknown}
}

// before reports whether r ranks ahead of o. Within a class an earlier time
// goes first, and a known time ahead of an unknown one.
func (r smartRank) before(o smartRank) bool {
	if r.class != o.class {
		return r.class < o.class
	}
	if r.at.IsZero() || o.at.IsZero() {
		return !r.at.IsZero() && o.at.IsZero()
	}
	return r.at.Before(o.at)
}

// smartLeaves is every concrete provider under a smart pool, at any depth.
func smartLeaves(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if !p.IsPool() || p.StrategyOrDefault() != config.StrategySmart {
			continue
		}
		for _, l := range cfg.Leaves(name) {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}

// refreshQuota reads the quota of every smart pool's members now, then every
// quotaEvery and whenever the config is reloaded, until ctx ends. Only smart
// pools cause a balance query; no other routing does.
func (s *Server) refreshQuota(ctx context.Context) {
	t := time.NewTicker(quotaEvery)
	defer t.Stop()
	for {
		s.readQuota(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.quotaKick:
		}
	}
}

// readQuota asks each smart member's source once per account and records it.
func (s *Server) readQuota(ctx context.Context) {
	cfg := s.config()
	leaves := smartLeaves(cfg)
	if len(leaves) == 0 {
		return
	}
	type ask struct {
		name    string
		p       config.Provider
		key     string
		members []string
	}
	byAccount := map[string]*ask{}
	var asks []*ask
	for _, l := range leaves {
		p := cfg.Providers[l]
		key, err := s.res.Resolve(p)
		if err != nil {
			continue
		}
		k := quota.DedupKey(p, key)
		if a := byAccount[k]; a != nil {
			a.members = append(a.members, l)
			continue
		}
		a := &ask{name: l, p: p, key: key, members: []string{l}}
		byAccount[k] = a
		asks = append(asks, a)
	}
	var wg sync.WaitGroup
	for _, a := range asks {
		wg.Add(1)
		go func(a *ask) {
			defer wg.Done()
			al := allowanceOf(s.queryQuota(ctx, a.name, a.p, a.key))
			s.st.mu.Lock()
			for _, m := range a.members {
				s.st.quota[m] = al
			}
			s.st.mu.Unlock()
		}(a)
	}
	wg.Wait()
}
