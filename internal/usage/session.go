package usage

import (
	"sort"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// Session is one engine session: what it used, and how much of the prompt the
// provider served from its prompt cache.
//
// CacheHit is nil when the session reported no cache activity. A provider
// without prompt caching and agy, which records no token counts at all, have
// nothing to say here, and a zero would read as a total miss.
type Session struct {
	Engine   string    `json:"engine"`
	ID       string    `json:"session"`
	Provider string    `json:"provider"`
	Tokens   Tokens    `json:"tokens"`
	Requests int       `json:"requests"`
	Last     time.Time `json:"last"`
	CacheHit *float64  `json:"cache_hit,omitempty"`
}

// HitRate is the share of prompt tokens served from cache: cache reads over
// input, cache reads and cache writes, the only split every engine agrees on.
// Writes count as prompt volume rather than hits, so a session that filled a
// cache and never reused it reports 0%.
func (s Session) HitRate() (float64, bool) {
	prompt := s.Tokens.Input + s.Tokens.CacheRead + s.Tokens.CacheWrite
	if prompt == 0 || s.Tokens.CacheRead+s.Tokens.CacheWrite == 0 {
		return 0, false
	}
	return float64(s.Tokens.CacheRead) / float64(prompt) * 100, true
}

// Sessions aggregates the logs into one entry per engine session.
func Sessions(cfg *config.Config) ([]Session, error) {
	buckets, a, err := loadBuckets(cfg)
	if err != nil {
		return nil, err
	}
	return sessions(buckets, a), nil
}

// sessions folds the buckets per (engine, session). Dedupe already gave a
// request to the file that first logged it, so a resumed session whose history
// was copied is counted once, as everywhere else here.
func sessions(bs []bucket, a *attribution) []Session {
	type key struct{ engine, id string }
	acc := map[key]*Session{}
	for _, b := range bs {
		k := key{b.Engine, b.Session}
		s := acc[k]
		if s == nil {
			s = &Session{Engine: b.Engine, ID: b.Session, Provider: providerOf(b, a)}
			acc[k] = s
		}
		if s.Provider == "" {
			s.Provider = providerOf(b, a)
		}
		s.Tokens.add(b.Tokens)
		s.Requests += b.Requests
		if b.Last.After(s.Last) {
			s.Last = b.Last
		}
	}

	out := make([]Session, 0, len(acc))
	for _, s := range acc {
		if h, ok := s.HitRate(); ok {
			s.CacheHit = &h
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Tokens.Total(), out[j].Tokens.Total(); a != b {
			return a > b
		}
		if out[i].Engine != out[j].Engine {
			return out[i].Engine < out[j].Engine
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// CacheNote says what a report of sessions cannot cover, since no row can show
// it: agy keeps conversations and steps, and no token or cache figure at all.
const CacheNote = "agy records no tokens or cache usage, so no antigravity row here has a cache hit"
