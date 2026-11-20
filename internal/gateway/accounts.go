package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/translate"
)

// quotaFresh is how long an account's reading is served again before its
// vendor is asked anew: clients poll, vendors rate-limit.
const quotaFresh = time.Minute

// readings caches each account's last quota reading.
type readings struct {
	mu sync.Mutex
	m  map[string]reading
}

type reading struct {
	q  quota.Quota
	at time.Time
}

// Quotas reads the accounts' quota, each at most once a minute, in parallel.
// Each reading names its account as Provider.
func (s *Server) Quotas(ctx context.Context, accounts []string) []quota.Quota {
	cfg := s.config()
	out := make([]quota.Quota, len(accounts))
	var wg sync.WaitGroup
	for i, a := range accounts {
		s.readings.mu.Lock()
		got, ok := s.readings.m[a]
		s.readings.mu.Unlock()
		if ok && time.Since(got.at) < quotaFresh {
			out[i] = got.q
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, ok := cfg.AccountProvider(a)
			if !ok {
				out[i] = quota.Quota{Provider: a, Error: "unknown vendor"}
				return
			}
			key, err := s.res.Resolve(p)
			if err != nil {
				out[i] = quota.Quota{Provider: a, Error: "no key"}
				return
			}
			q := s.queryQuota(ctx, a, p, key)
			out[i] = q
			s.readings.mu.Lock()
			s.readings.m[a] = reading{q: q, at: time.Now()}
			s.readings.mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// handleQuota serves GET /ak/v1/quota: the readings of the accounts the
// calling token reaches.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	_, tok, ok := caller(cfg, r)
	if !ok {
		apiError(w, translate.Chat, http.StatusUnauthorized, "ak: no valid token")
		return
	}
	writeJSON(w, map[string]any{"accounts": s.Quotas(r.Context(), accountsOf(cfg, tok))})
}
