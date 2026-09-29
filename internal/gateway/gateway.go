// Package gateway is ak's local routing gateway for provider pools.
//
// A pool is a provider whose Members are other providers of the same kind. Its
// generated command points the engine at the gateway instead of an upstream, so
// the gateway sees every request, picks a member by the pool's strategy, injects
// that member's real key and hides it from the command. It speaks the same
// protocol as the engine: requests and responses pass through unchanged, except
// for the model name when a member maps one. Protocol translation is out of
// scope.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/secrets"
)

// halfLife is how fast a member's served count stops counting against it when
// choosing by least-used: half after an hour.
const halfLife = time.Hour

// Cooldown bounds after a failure.
const (
	baseCooldown = 30 * time.Second
	maxCooldown  = 10 * time.Minute
)

// Server routes pool requests.
type Server struct {
	cfg    *config.Config
	res    secrets.Resolver
	client *http.Client
	st     *state

	// Logf, when set, logs routing decisions.
	Logf func(format string, a ...any)
}

// New builds a server over a config.
func New(cfg *config.Config) *Server {
	tr, _ := http.DefaultTransport.(*http.Transport)
	t := tr.Clone()
	t.ResponseHeaderTimeout = 90 * time.Second
	return &Server{
		cfg:    cfg,
		res:    secrets.Default(),
		client: &http.Client{Transport: t},
		st:     newState(),
	}
}

// Handler returns the gateway's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/p/", s.handlePool)
	return mux
}

// Serve listens on addr and serves until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	s.logf("gateway listening on http://%s", ln.Addr())
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) logf(format string, a ...any) {
	if s.Logf != nil {
		s.Logf(format, a...)
		return
	}
	log.Printf(format, a...)
}

// handlePool serves /p/<pool>/<rest>.
func (s *Server) handlePool(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/p/")
	name, tail, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		http.Error(w, "ak: malformed pool path", http.StatusBadRequest)
		return
	}
	p, found := s.cfg.Providers[name]
	if !found || !p.IsPool() {
		http.Error(w, "ak: no pool named "+name, http.StatusNotFound)
		return
	}

	// Buffer the request body once: failover needs to replay it, and a model
	// rewrite needs to read it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "ak: reading request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	r.Body.Close()

	members := s.st.candidates(name, p)
	if len(members) == 0 {
		http.Error(w, "ak: pool "+name+" has no members", http.StatusServiceUnavailable)
		return
	}

	var lastErr string
	for i, m := range members {
		mp := s.cfg.Providers[m]
		_, retryable, err := s.forward(w, r, name, m, tail, mp, body)
		if err == nil {
			s.st.succeeded(name, m)
			return
		}
		lastErr = err.Error()
		if !retryable {
			return
		}
		s.st.failed(name, m)
		s.logf("pool %s: member %s failed (%s); trying next", name, m, lastErr)
		if i == len(members)-1 {
			break
		}
	}
	// Every member failed before any bytes were written.
	http.Error(w, "ak: pool "+name+" exhausted its members: "+lastErr, http.StatusBadGateway)
}

// forward sends the request to one member. It returns whether the failure is
// retryable; a retryable failure is one where nothing was written to the client
// yet, so the caller may try the next member.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, pool, member, tail string, mp config.Provider, body []byte) (status int, retryable bool, err error) {
	target := strings.TrimRight(mp.BaseURL, "/") + "/" + tail
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	out := body
	if model := mpModel(member, s.cfg.Providers[pool]); model != "" {
		out = rewriteModel(body, model)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(out))
	if err != nil {
		return 0, true, err
	}
	copyHeaders(req.Header, r.Header)
	key, err := s.res.Resolve(mp)
	if err != nil {
		return 0, false, fmt.Errorf("key for member %s: %w", member, err)
	}
	setAuth(req.Header, mp, key)

	resp, err := s.client.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return 0, false, r.Context().Err() // the client went away
		}
		return 0, true, err
	}
	defer resp.Body.Close()

	if isRetryableStatus(resp.StatusCode) {
		drain(resp.Body)
		return resp.StatusCode, true, fmt.Errorf("upstream %s returned %d", mp.BaseURL, resp.StatusCode)
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	copyFlush(w, resp.Body)
	return resp.StatusCode, false, nil
}

// mpModel is the model to ask the member for: the pool's mapping for it, else
// empty meaning pass the request's model through.
func mpModel(member string, pool config.Provider) string {
	if pool.MemberModels == nil {
		return ""
	}
	return pool.MemberModels[member]
}

// rewriteModel replaces the body's model field. A body that is not a JSON
// object, or has no model, is returned unchanged.
func rewriteModel(body []byte, model string) []byte {
	if len(body) == 0 {
		return body
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	if _, ok := doc["model"]; !ok {
		return body
	}
	doc["model"] = model
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// setAuth writes the member's key the way its engine expects and drops any
// incoming auth, so the pool placeholder never reaches an upstream.
func setAuth(h http.Header, mp config.Provider, key string) {
	h.Del("Authorization")
	h.Del("X-Api-Key")
	if key == "" {
		return
	}
	if mp.Kind == config.KindClaude && mp.KeyField == "api_key" {
		h.Set("X-Api-Key", key)
		return
	}
	h.Set("Authorization", "Bearer "+key)
}

// copyHeaders copies src into dst, skipping hop-by-hop headers that must not be
// forwarded.
func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop(k) {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func hopByHop(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

// isRetryableStatus reports whether a response status means "try the next
// member": rate limits, payment, server errors and Anthropic's overload.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusPaymentRequired,
		http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// drain reads and discards a small amount of a failed response so the
// connection can be reused.
func drain(r io.Reader) { io.CopyN(io.Discard, r, 1<<16) }

// copyFlush streams the upstream body to the client, flushing after each read
// so server-sent events arrive as they are produced.
func copyFlush(w http.ResponseWriter, r io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// state is the gateway's in-memory view of member health and use.
type state struct {
	mu    sync.Mutex
	cool  map[string]time.Time // pool\x00member -> cooling until
	fails map[string]int       // pool\x00member -> consecutive failures
	turns map[string]int       // pool -> rotate counter
	used  map[string]tokenUse  // pool\x00member -> decayed served count
}

type tokenUse struct {
	n  float64
	at time.Time
}

func newState() *state {
	return &state{
		cool:  map[string]time.Time{},
		fails: map[string]int{},
		turns: map[string]int{},
		used:  map[string]tokenUse{},
	}
}

func key(pool, member string) string { return pool + "\x00" + member }

func (u tokenUse) now(t time.Time) float64 {
	return u.n * math.Exp2(-t.Sub(u.at).Seconds()/halfLife.Seconds())
}

// candidates orders a pool's members for one request: healthy members by the
// strategy first, then cooling ones by when they come back.
func (s *state) candidates(pool string, p config.Provider) []string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var healthy, cooling []string
	for _, m := range p.Members {
		if until, ok := s.cool[key(pool, m)]; ok && until.After(now) {
			cooling = append(cooling, m)
			continue
		}
		healthy = append(healthy, m)
	}
	sort.SliceStable(cooling, func(i, j int) bool {
		return s.cool[key(pool, cooling[i])].Before(s.cool[key(pool, cooling[j])])
	})

	switch p.StrategyOrDefault() {
	case config.StrategyRotate:
		if n := len(healthy); n > 1 {
			k := s.turns[pool] % n
			s.turns[pool]++
			healthy = append(append([]string{}, healthy[k:]...), healthy[:k]...)
		}
	case config.StrategyLeastUsed:
		sort.SliceStable(healthy, func(i, j int) bool {
			return s.used[key(pool, healthy[i])].now(now) < s.used[key(pool, healthy[j])].now(now)
		})
	}
	return append(healthy, cooling...)
}

func (s *state) succeeded(pool, member string) {
	k := key(pool, member)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cool, k)
	delete(s.fails, k)
	u := s.used[k]
	s.used[k] = tokenUse{n: u.now(now) + 1, at: now}
}

func (s *state) failed(pool, member string) {
	k := key(pool, member)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[k]++
	d := baseCooldown << min(s.fails[k]-1, 10)
	if d > maxCooldown {
		d = maxCooldown
	}
	s.cool[k] = time.Now().Add(d)
}
