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
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
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

// Bounds on a proxied request and its streams.
const (
	// maxBodyBytes is the largest request body buffered: failover replays it,
	// so it is held in memory and capped. A long prompt is a few megabytes;
	// 32 holds a whole one with room, and 256 of them in flight stay bounded.
	maxBodyBytes = 32 << 20
	// bodyReadTimeout is how long a client has to send its whole body.
	bodyReadTimeout = 60 * time.Second
	// maxErrorBody is how much of a 4xx body is read to classify its cause.
	maxErrorBody = 64 << 10
	// streamIdle is how long a stream may stall with no bytes before it is cut.
	streamIdle = 2 * time.Minute
	// streamHead is how much of an event stream is held back before its first
	// content, so an error that opens the stream can fail over.
	streamHead = 256 << 10
)

// Tunables for how long an upstream may take to answer with its headers. A
// stream opens quickly, so a silent member is left early; a plain request may
// legitimately think for minutes before its first byte, and since its reply is
// still being produced (and billed), it is not replayed elsewhere.
var (
	streamHeaderWait = 90 * time.Second
	plainHeaderWait  = 10 * time.Minute
)

// errHeaderWait is the cause recorded when an upstream sent no headers in time.
var errHeaderWait = errors.New("no response headers in time")

// errClientWrite marks a failure to write to the client: the agent hung up or
// was interrupted, which says nothing about the member.
var errClientWrite = errors.New("client write failed")

// Affinity bounds: how long a conversation's answerer is remembered, how many
// conversations are kept, and when staying there is worth it.
const (
	affinityTTL = 24 * time.Hour
	affinityMax = 512
	// affinityWarm is how long a conversation may sit idle before the vendor
	// has let go of its prompt. Past it nothing cached is lost by moving, so
	// the conversation is free to go where routing puts it. Anthropic and
	// OpenAI both keep a prefix about five minutes.
	affinityWarm = 5 * time.Minute
	// affinityWorth is how many tokens an answer read from the vendor's cache
	// or wrote to it to be worth keeping the conversation where it answered.
	// Shorter conversations leave no cache worth reading again, so pinning
	// them costs the strategy's choice and buys nothing.
	affinityWorth = 1024
	// cacheUnknown marks a conversation whose cache was never read: a pool's
	// own gateway does not tap its replies, so it keeps as it always has.
	cacheUnknown = -1
)

// Tunables, package variables so tests can shrink them.
var (
	// sameMemberRetries is how many times a transient failure retries the same
	// member before the request falls through to the next.
	sameMemberRetries = 1
	// retryPause is the pause before the first such retry; each after doubles.
	retryPause = time.Second
	// maxRetryWait bounds a retry pause so failover is not delayed past it.
	maxRetryWait = 8 * time.Second
)

// Server routes pool requests.
type Server struct {
	cfg    atomic.Pointer[config.Config]
	res    secrets.Resolver
	client *http.Client
	st     *state
	lanes  *lanes
	live   gate // how many requests are in flight

	// affinityPath is where the conversation table is kept between runs.
	affinityPath string

	// Logf, when set, logs routing decisions.
	Logf func(format string, a ...any)

	// queryQuota reads a provider's quota for smart pools; quotaKick asks for
	// a fresh reading, as after a reload.
	queryQuota func(ctx context.Context, name string, p config.Provider, key string) quota.Quota
	quotaKick  chan struct{}

	// The service's routes as pools (see routing), its own health and
	// affinity state, its usage and its accounts' last quota readings.
	svc             atomic.Pointer[config.Config]
	sst             *state
	svcAffinityPath string
	meter           *meter
	readings        readings
	// lat holds the service's recent timings per route.
	lat *latencies

	// UI, when set, is served by the service under /ui/.
	UI http.Handler
}

// New builds a server over a config.
func New(cfg *config.Config) *Server {
	tr, _ := http.DefaultTransport.(*http.Transport)
	t := tr.Clone()
	// An agent fires several requests at one vendor at once; the default of
	// two idle connections per host makes the rest pay a TCP and TLS
	// handshake every time.
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	s := &Server{
		res:    secrets.Cached(secrets.Default()),
		client: &http.Client{Transport: t, CheckRedirect: sameHost},
		st:     newState(),
		lanes:  newLanes(),

		queryQuota: quota.Query,
		quotaKick:  make(chan struct{}, 1),
		sst:        newState(),
		readings:   readings{m: map[string]reading{}},
		lat:        newLatencies(),
	}
	s.cfg.Store(cfg)
	s.svc.Store(routing(cfg))
	dir, err := config.DataDir()
	if err == nil {
		s.affinityPath = filepath.Join(dir, "affinity.json")
		s.svcAffinityPath = filepath.Join(dir, "service-affinity.json")
	}
	s.meter = newMeter(dir)
	return s
}

// config is the current config, which Reload may have replaced.
func (s *Server) config() *config.Config { return s.cfg.Load() }

// Config is the config the server runs on; it must not be changed in place.
func (s *Server) Config() *config.Config { return s.config() }

// Reload swaps in a new config; the running gateway picks up an edited pool
// without a restart.
func (s *Server) Reload(cfg *config.Config) {
	s.cfg.Store(cfg)
	s.svc.Store(routing(cfg))
	s.logf("gateway reloaded %d provider(s)", len(cfg.Providers))
	select {
	case s.quotaKick <- struct{}{}:
	default:
	}
}

// Log writes a gateway message where Logf says to, else to the standard log.
func (s *Server) Log(format string, a ...any) { s.logf(format, a...) }

// Handler returns the gateway's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/p/", s.handlePool)
	return mux
}

// handleHealth reports liveness and each pool member's state. Unlike /stats it
// is cheap and carries no traffic series, so a supervisor can poll it.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	now := time.Now()
	s.st.mu.Lock()
	type memberHealth struct {
		Cooling      bool       `json:"cooling"`
		CoolingUntil *time.Time `json:"cooling_until,omitempty"`
		OK           int64      `json:"ok"`
		Fail         int64      `json:"fail"`
		// Spent and Back are the last quota reading, for smart pools.
		Spent *bool      `json:"spent,omitempty"`
		Back  *time.Time `json:"back,omitempty"`
	}
	pools := map[string]map[string]memberHealth{}
	for name, p := range cfg.Providers {
		if !p.IsPool() {
			continue
		}
		ms := map[string]memberHealth{}
		for _, m := range cfg.Leaves(name) {
			k := key(name, m)
			h := memberHealth{OK: s.st.okN[k], Fail: s.st.failN[k]}
			if a := s.st.quota[m]; a.known {
				h.Spent, h.Back = &a.spent, a.back
			}
			if until, ok := s.st.cool[k]; ok && until.After(now) {
				u := until
				h.Cooling, h.CoolingUntil = true, &u
			}
			ms[m] = h
		}
		pools[name] = ms
	}
	s.st.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "at": now, "pools": pools})
}

// Serve listens on addr for local pools and, when service is not empty,
// on service for other machines, until ctx is cancelled. The service is
// refused on an address other machines reach while no token is defined.
func (s *Server) Serve(ctx context.Context, addr, service string) error {
	if service != "" {
		if err := CheckExposure(service, s.config()); err != nil {
			return err
		}
	}
	// Keep the conversation tables on disk, so a restart does not lose the
	// vendor's prompt cache for every session. Loaded before listening, so an
	// early request's entry is not overwritten by the older file.
	s.st.loadSticks(s.affinityPath)
	s.sst.loadSticks(s.svcAffinityPath)
	s.meter.load()
	errCh := make(chan error, 2)
	var servers []*http.Server
	for _, l := range []struct {
		addr, what string
		h          http.Handler
	}{{addr, "gateway", s.Handler()}, {service, "service", s.ServiceHandler()}} {
		if l.addr == "" {
			continue
		}
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			for _, srv := range servers {
				srv.Close()
			}
			return err
		}
		srv := &http.Server{
			Handler:           l.h,
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
		s.logf("%s listening on http://%s", l.what, ln.Addr())
	}
	stopFlush := make(chan struct{})
	go s.flushSticks(ctx, stopFlush)
	go s.refreshQuota(ctx)

	save := func() {
		s.st.saveSticks(s.affinityPath)
		s.sst.saveSticks(s.svcAffinityPath)
		s.meter.save()
		close(stopFlush)
	}
	select {
	case <-ctx.Done():
		save()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		for _, srv := range servers {
			err = errors.Join(err, srv.Shutdown(shutCtx))
		}
		return err
	case err := <-errCh:
		save()
		for _, srv := range servers {
			srv.Close()
		}
		return err
	}
}

// flushSticks writes the conversation table periodically when it changed.
func (s *Server) flushSticks(ctx context.Context, stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.st.saveSticks(s.affinityPath)
			s.sst.saveSticks(s.svcAffinityPath)
			s.meter.save()
		case <-ctx.Done():
			return
		case <-stop:
			return
		}
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
	cfg := s.config()
	rest := strings.TrimPrefix(r.URL.Path, "/p/")
	name, tail, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		http.Error(w, "ak: malformed pool path", http.StatusBadRequest)
		return
	}
	p, found := cfg.Providers[name]
	if !found || !p.IsPool() {
		http.Error(w, "ak: no pool named "+name, http.StatusNotFound)
		return
	}

	// Refuse rather than queue when the gateway is already saturated, so one
	// runaway caller cannot starve the machine.
	s.live.setLimit(cfg.Settings.MaxInflight)
	if !s.live.enter() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "ak: gateway is at its in-flight limit; retry shortly", http.StatusServiceUnavailable)
		return
	}
	defer s.live.leave()

	// Buffer the request body once: failover needs to replay it, and a model
	// rewrite needs to read it.
	body, ok := readBody(w, r)
	if !ok {
		return
	}

	conv := conversationKey(r, body)
	routes := s.st.candidates(cfg, name, p, conv)
	if len(routes) == 0 {
		http.Error(w, "ak: pool "+name+" has no members", http.StatusServiceUnavailable)
		return
	}
	build := func(rt route) (leg, error) {
		mp := cfg.Providers[rt.member]
		target := strings.TrimRight(mp.BaseURL, "/") + "/" + tail
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		out := body
		if rt.model != "" {
			out = rewriteModel(body, rt.model)
		}
		return leg{member: rt.member, p: mp, target: target, body: out}, nil
	}
	s.relay(w, r, s.st, name, routes, conv, build, func(status int, retryAfter time.Duration, msg string) {
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		}
		http.Error(w, "ak: pool "+name+msg, status)
	})
}

// leg is one way to answer a request: the concrete provider whose key it
// carries and whose lane it waits in, where it goes and what it sends.
type leg struct {
	member string
	p      config.Provider
	target string
	body   []byte
	// reply, when set, stands between the upstream's reply and the client,
	// as a translation does; finish is called once the reply is written.
	reply func(http.ResponseWriter) replyWriter
	// header, when set, adjusts what goes upstream once the key is in.
	header func(http.Header)
}

// replyWriter is a ResponseWriter that may hold back what it was given
// until finish.
type replyWriter interface {
	http.ResponseWriter
	http.Flusher
	finish()
}

// relay sends a request down routes in order until one answers, keeping
// health, use and affinity in st under name, the pool or route asked for.
// build makes each route's leg; a route it cannot build is passed over.
// When none answers, fail is told the status and the reason, which starts
// with ": " or " ". It returns the member that answered, or "".
func (s *Server) relay(w http.ResponseWriter, r *http.Request, st *state, name string, routes []route,
	conv string, build func(route) (leg, error), fail func(status int, retryAfter time.Duration, msg string)) string {
	var lastErr string
	var tried []string
	// lastStatus and lastRetryAfter are the last answer a member gave, so a
	// rate limit reaches the agent as a rate limit with its Retry-After,
	// not as a gateway error it cannot back off from.
	lastStatus, lastRetryAfter := 0, time.Duration(0)
	unbuilt := 0
	for i, rt := range routes {
		lg, err := build(rt)
		if err != nil {
			unbuilt++
			lastErr = err.Error()
			tried = append(tried, rt.member+": "+lastErr)
			s.logf("%s: member %s cannot serve the request (%s); trying next", name, rt.member, lastErr)
			continue
		}
		m := lg.member
		// A member with a concurrency bound queues its turn; waiting is not
		// failing and never moves the request to another member.
		limit := lg.p.MaxConcurrency
		if limit <= 0 {
			limit = s.config().Settings.MaxConcurrency
		}
		release, ok := s.lanes.acquire(r.Context(), m, limit)
		if !ok {
			return "" // the client went away while waiting
		}
		var res forwardOutcome
		for attempt := 0; ; attempt++ {
			out, rw := w, replyWriter(nil)
			if lg.reply != nil {
				rw = lg.reply(w)
				out = rw
			}
			res = s.forward(out, r, lg)
			if res.handled {
				if rw != nil {
					rw.finish()
				}
				release()
				switch {
				case res.ok:
					st.succeeded(name, m, conv)
					return m
				case res.err != nil && !clientGone(r, res.err):
					// A reply that broke after it began: it cannot be retried, but
					// the member that cut it is set aside for the next request.
					st.failed(name, m, 0)
				}
				return m
			}
			if clientGone(r, res.err) {
				// The agent hung up or was interrupted: not the member's fault,
				// and nobody is left to answer.
				release()
				return ""
			}
			if !res.retryable {
				break
			}
			// A busy vendor often answers the same member a moment later; try
			// it again before giving up on it.
			if attempt < sameMemberRetries && retrySame(res) {
				wait := retryBackoff(attempt, res.retryAfter)
				if wait <= maxRetryWait {
					s.logf("%s: member %s failed (%s); retrying in %s", name, m, res.err, wait)
					select {
					case <-time.After(wait):
						continue
					case <-r.Context().Done():
						release()
						return ""
					}
				}
			}
			break
		}
		release()
		if res.err == nil {
			res.err = errors.New("no response")
		}
		if !res.retryable {
			// Nothing was written and no other member can help (a request
			// that already took the member past its wait): tell the caller
			// instead of an empty 200.
			fail(http.StatusGatewayTimeout, res.retryAfter, ": "+res.err.Error())
			return ""
		}
		lastErr = res.err.Error()
		if res.status >= 400 {
			lastStatus, lastRetryAfter = res.status, res.retryAfter
		}
		if len(tried) < 8 {
			tried = append(tried, m+": "+lastErr)
		}
		st.failed(name, m, res.retryAfter)
		s.logf("%s: member %s failed (%s); trying next", name, m, lastErr)
		if i == len(routes)-1 {
			break
		}
	}
	// Every member failed before any bytes were written. When none could even
	// be asked, the request itself is what no member can take.
	status := http.StatusBadGateway
	if unbuilt == len(routes) {
		status = http.StatusBadRequest
	} else if passThrough(lastStatus) {
		status = lastStatus
	}
	fail(status, lastRetryAfter, " exhausted its members: "+lastErr+"\n  "+strings.Join(tried, "\n  "))
	return ""
}

// readCloser closes the body underneath a decompressor with it.
type readCloser struct {
	io.Reader
	io.Closer
}

// passThrough reports whether a member's own status is the one to answer
// with when every member failed: a rate limit or a spent plan says when to
// come back, which a 502 does not.
func passThrough(status int) bool {
	switch status {
	case http.StatusPaymentRequired, http.StatusTooManyRequests,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// readBody reads and bounds the request body. It writes the error itself and
// answers false when the body cannot be used.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > maxBodyBytes {
		http.Error(w, "ak: request body exceeds the gateway limit", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	ctl := http.NewResponseController(w)
	_ = ctl.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	defer ctl.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "ak: request body exceeds the gateway limit", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "ak: reading request body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	r.Body.Close()
	return body, true
}

// forwardOutcome is what one attempt at one member produced.
type forwardOutcome struct {
	status     int
	retryAfter time.Duration
	// retryable means nothing was written to the client, so another member
	// (or the same one again) may answer.
	retryable bool
	// handled means a response was written to the client; stop.
	handled bool
	// ok means that response was the upstream's success.
	ok  bool
	err error
}

// forward sends one leg of a request.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, lg leg) forwardOutcome {
	member, mp, target, out := lg.member, lg.p, lg.target, lg.body

	// Each member has its own key: one that cannot be resolved sets that
	// member aside, and the next may still answer.
	key, err := s.res.Resolve(mp)
	if err != nil {
		return forwardOutcome{retryable: true, err: fmt.Errorf("key for member %s: %w", member, err)}
	}

	// The wait for headers is per request: a stream gets a short one, a plain
	// request a long one. Cancelling ctx also ends the body, so it is held
	// until forward returns.
	stream := isStreamRequest(out)
	wait := plainHeaderWait
	if stream {
		wait = streamHeaderWait
	}
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	headerTimer := time.AfterFunc(wait, func() { cancel(errHeaderWait) })

	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(out))
	if err != nil {
		headerTimer.Stop()
		return forwardOutcome{retryable: true, err: err}
	}
	copyHeaders(req.Header, r.Header)
	// Let the transport negotiate compression itself, so it decompresses the
	// reply: a gzipped event stream or error body could not be inspected.
	// A stream is the exception: compressing it buys nothing at the size of
	// an event, and the decompressor is another buffer between the token and
	// the agent.
	req.Header.Del("Accept-Encoding")
	if stream {
		req.Header.Set("Accept-Encoding", "identity")
	}
	setAuth(req.Header, mp, key)
	if lg.header != nil {
		lg.header(req.Header)
	}

	resp, err := s.client.Do(req)
	headerTimer.Stop()
	if err != nil {
		if r.Context().Err() != nil {
			return forwardOutcome{err: r.Context().Err()} // the client went away
		}
		if context.Cause(ctx) == errHeaderWait {
			err = fmt.Errorf("upstream %s: %w after %s", mp.BaseURL, errHeaderWait, wait)
			// A plain request may still be running, and billed, upstream:
			// replaying it elsewhere would pay for it twice.
			return forwardOutcome{retryable: stream, err: err}
		}
		return forwardOutcome{retryable: true, err: err}
	}
	defer resp.Body.Close()

	// A stream asked for identity, so an upstream that gzips anyway was not
	// read by the transport: decompress it here, so the head of a stream can
	// still be inspected before it is committed.
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		if zr, err := gzip.NewReader(resp.Body); err == nil {
			resp.Body = readCloser{zr, resp.Body}
			resp.Header.Del("Content-Encoding")
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1
		}
	}

	code := resp.StatusCode
	if isRetryableStatus(code) {
		drain(resp.Body)
		return forwardOutcome{status: code, retryAfter: retryAfterOf(resp.Header), retryable: true,
			err: fmt.Errorf("upstream %s returned %d", mp.BaseURL, code)}
	}
	// A 400/422 may mean "this member cannot serve the request" rather than
	// "the request is wrong"; the body says which.
	if code == http.StatusBadRequest || code == http.StatusUnprocessableEntity {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if retryableBody(buf) {
			return forwardOutcome{status: code, retryable: true,
				err: fmt.Errorf("upstream %s returned %d: %s", mp.BaseURL, code, snippet(buf))}
		}
		copyHeaders(w.Header(), resp.Header)
		w.Header().Del("Content-Length")
		w.WriteHeader(code)
		w.Write(buf)
		return forwardOutcome{status: code, handled: true}
	}

	if isEventStream(resp.Header) {
		return streamSSE(w, resp)
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(code)
	err = copyIdle(w, resp.Body)
	return forwardOutcome{status: code, handled: true, ok: err == nil, err: err}
}

// retryableBody classifies a 400/422 body: a request the next member may serve
// better (an unserved model, a quota, a shape this vendor cannot read, a
// channel it refuses) is retryable; one every member would refuse — a
// conversation too long, a missing field — is not.
func retryableBody(body []byte) bool {
	if overflowWords.Match(body) {
		return false
	}
	return quotaWords.Match(body) || unservedWords.Match(body) ||
		refusedWords.Match(body) || shapeWords.Match(body)
}

var (
	quotaWords    = regexp.MustCompile(`(?i)insufficient|quota|out of credit|no credit|balance|billing|payment required|exceeded your current`)
	unservedWords = regexp.MustCompile(`(?i)not accessible|does not exist|doesn't exist|not found|no such model|unsupported model|model_not_found|not available|not supported`)
	refusedWords  = regexp.MustCompile(`(?i)unapproved channel|illegal api invocation`)
	shapeWords    = regexp.MustCompile(`(?i)failed to deserialize|unknown (item |content |input )?(type|variant|field|parameter)|unknown_parameter|unrecognized (request argument|field|parameter)|additional properties are not allowed`)
	overflowWords = regexp.MustCompile(`(?i)context length|maximum context|too long|prompt is too long|context window|max_tokens`)
)

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// rewriteModel replaces the body's top-level model field, leaving every other
// byte — number precision, string escaping, key order — as it was. A body that
// is not a JSON object, or has no string model, is returned unchanged.
func rewriteModel(body []byte, model string) []byte {
	vs, ve, ok := findTopLevelValue(body, "model")
	if !ok || vs >= len(body) || body[vs] != '"' {
		return body
	}
	quoted, err := json.Marshal(model)
	if err != nil {
		return body
	}
	out := make([]byte, 0, len(body)-(ve-vs)+len(quoted))
	out = append(out, body[:vs]...)
	out = append(out, quoted...)
	out = append(out, body[ve:]...)
	return out
}

// findTopLevelValue returns the byte range of a top-level object key's value.
// It scans without decoding, so nothing else in the document is disturbed.
func findTopLevelValue(body []byte, key string) (int, int, bool) {
	i, n := 0, len(body)
	skipWS := func() {
		for i < n && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
			i++
		}
	}
	skipWS()
	if i >= n || body[i] != '{' {
		return 0, 0, false
	}
	i++
	for {
		skipWS()
		if i >= n || body[i] == '}' {
			return 0, 0, false
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return 0, 0, false
		}
		ke, ok := skipString(body, i)
		if !ok {
			return 0, 0, false
		}
		k := string(body[i:ke])
		i = ke
		skipWS()
		if i >= n || body[i] != ':' {
			return 0, 0, false
		}
		i++
		skipWS()
		vs := i
		ve, ok := skipValue(body, vs)
		if !ok {
			return 0, 0, false
		}
		if k == `"`+key+`"` {
			return vs, ve, true
		}
		i = ve
	}
}

// skipString advances past a JSON string starting at i, returning the index
// just past its closing quote.
func skipString(b []byte, i int) (int, bool) {
	if i >= len(b) || b[i] != '"' {
		return i, false
	}
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1, true
		default:
			i++
		}
	}
	return i, false
}

// skipValue advances past the JSON value starting at i.
func skipValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return i, false
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				ni, ok := skipString(b, i)
				if !ok {
					return i, false
				}
				i = ni
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return i, false
	default:
		for i < len(b) && b[i] != ',' && b[i] != '}' && b[i] != ']' {
			i++
		}
		return i, true
	}
}

// isEventStream reports whether a response body is server-sent events.
func isEventStream(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

// streamSSE serves an event stream, holding the beginning back until its first
// content so an error that opens the stream fails over instead of reaching the
// agent. Nothing is written before that, so a failure there is retryable.
func streamSSE(w http.ResponseWriter, resp *http.Response) forwardOutcome {
	flusher, _ := w.(http.Flusher)
	timer := time.AfterFunc(streamIdle, func() { resp.Body.Close() })
	defer timer.Stop()

	var head []byte
	scan := 0
	committed := false
	buf := make([]byte, 16<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			timer.Reset(streamIdle)
			if committed {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return forwardOutcome{handled: true, err: fmt.Errorf("%w: %v", errClientWrite, werr)}
				}
				if flusher != nil {
					flusher.Flush()
				}
			} else {
				head = append(head, buf[:n]...)
				var content, failure bool
				scan, content, failure = scanEvents(head, scan)
				if failure {
					return forwardOutcome{retryable: true,
						err: errors.New("upstream stream failed before its content")}
				}
				if content || len(head) >= streamHead {
					copyHeaders(w.Header(), resp.Header)
					w.Header().Set("Cache-Control", "no-cache")
					// nginx buffers a proxied response unless told not to.
					w.Header().Set("X-Accel-Buffering", "no")
					w.WriteHeader(resp.StatusCode)
					if _, werr := w.Write(head); werr != nil {
						return forwardOutcome{handled: true, err: fmt.Errorf("%w: %v", errClientWrite, werr)}
					}
					if flusher != nil {
						flusher.Flush()
					}
					committed = true
				}
			}
		}
		if rerr != nil {
			if !committed {
				if errors.Is(rerr, io.EOF) && len(head) > 0 {
					// A stream that ended cleanly before any marker we know is
					// still a complete reply; replaying it would bill it twice.
					copyHeaders(w.Header(), resp.Header)
					w.WriteHeader(resp.StatusCode)
					if _, werr := w.Write(head); werr != nil {
						return forwardOutcome{handled: true, err: fmt.Errorf("%w: %v", errClientWrite, werr)}
					}
					return forwardOutcome{handled: true, ok: true, status: resp.StatusCode}
				}
				return forwardOutcome{retryable: true, err: rerr}
			}
			if errors.Is(rerr, io.EOF) {
				return forwardOutcome{handled: true, ok: true, status: resp.StatusCode}
			}
			return forwardOutcome{handled: true, status: resp.StatusCode, err: rerr}
		}
	}
}

// scanEvents walks the complete events in buf from `from`, returning the new
// offset and whether one carried content or was the stream failing.
func scanEvents(buf []byte, from int) (int, bool, bool) {
	for {
		i, sep := eventBoundary(buf[from:])
		if i < 0 {
			return from, false, false
		}
		ev := buf[from : from+i]
		from += i + sep
		content, failure := sseEventKind(ev)
		if failure {
			return from, false, true
		}
		if content {
			return from, true, false
		}
	}
}

// eventBoundary is the index and width of the blank line ending an event.
func eventBoundary(b []byte) (int, int) {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\n' && b[i+1] == '\n' {
			return i, 2
		}
		if i+3 < len(b) && b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i, 4
		}
	}
	return -1, 0
}

// sseEventKind says whether one server-sent event carries content the reader
// will see, or is the stream reporting a failure.
func sseEventKind(ev []byte) (content, failure bool) {
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		if bytes.HasPrefix(ln, []byte("data:")) {
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return false, false
	}
	var v struct {
		Type   string          `json:"type"`
		Object string          `json:"object"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &v) != nil {
		return hasContentMarker(data), false
	}
	if v.Type == "error" || v.Object == "error" ||
		(len(v.Error) > 0 && !bytes.Equal(v.Error, []byte("null"))) {
		// An error every member would give (a conversation too long) is
		// passed to the agent like content rather than failed over.
		if overflowWords.Match(data) {
			return true, false
		}
		return false, true
	}
	return hasContentMarker(data), false
}

// hasContentMarker spots the first content of any protocol an agent speaks:
// text, reasoning or a tool call.
func hasContentMarker(data []byte) bool {
	for _, m := range [][]byte{
		[]byte(`"delta"`),
		[]byte(`"content_block_start"`),
		[]byte(`"output_item.added"`),
		[]byte(`"response.output_text"`),
		[]byte(`"response.reasoning"`),
		[]byte(`"response.content_part"`),
		[]byte(`"parts"`),
		[]byte(`"tool_calls"`),
		[]byte(`"function_call"`),
	} {
		if bytes.Contains(data, m) {
			return true
		}
	}
	return false
}

// copyIdle streams a body to the client, flushing after each read and cutting
// it when no bytes arrive within streamIdle.
func copyIdle(w http.ResponseWriter, r io.ReadCloser) error {
	flusher, _ := w.(http.Flusher)
	timer := time.AfterFunc(streamIdle, func() { r.Close() })
	defer timer.Stop()
	buf := make([]byte, 16<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			timer.Reset(streamIdle)
			if _, werr := w.Write(buf[:n]); werr != nil {
				return fmt.Errorf("%w: %v", errClientWrite, werr)
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// sameHost follows a redirect only while it stays on the host the request was
// given to: Go drops Authorization and Cookie across hosts but not x-api-key,
// and the key we put there is the vendor's, so a redirect must not carry it.
func sameHost(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return fmt.Errorf("ak: refusing a redirect from %s to %s", via[0].URL.Host, req.URL.Host)
	}
	if len(via) >= 10 {
		return fmt.Errorf("ak: refusing to follow more than 10 redirects")
	}
	return nil
}

// setAuth writes the member's key the way its engine expects and drops any
// incoming auth, so the pool placeholder never reaches an upstream.
func setAuth(h http.Header, mp config.Provider, key string) {
	h.Del("Authorization")
	h.Del("X-Api-Key")
	if key == "" {
		return
	}
	if usesAPIKeyHeader(mp) {
		h.Set("X-Api-Key", key)
		return
	}
	h.Set("Authorization", "Bearer "+key)
}

// usesAPIKeyHeader reports whether a member's engine sends its key as
// x-api-key: claude with key_field api_key, and pi on the Anthropic API unless
// it is told to send a bearer token.
func usesAPIKeyHeader(mp config.Provider) bool {
	switch mp.Kind {
	case config.KindClaude:
		return mp.KeyField == "api_key"
	case config.KindPi:
		return (mp.PiAPI == "" || mp.PiAPI == "anthropic-messages") && !mp.PiAuthHeader
	}
	return false
}

// isStreamRequest reports whether a request body asks for a streamed reply.
func isStreamRequest(body []byte) bool {
	vs, ve, ok := findTopLevelValue(body, "stream")
	return ok && string(bytes.TrimSpace(body[vs:ve])) == "true"
}

// clientGone reports whether a failure was the client's: it hung up, or a
// write to it failed.
func clientGone(r *http.Request, err error) bool {
	return r.Context().Err() != nil || errors.Is(err, errClientWrite)
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
// member": an auth or routing failure of this one, rate limits, payment,
// server errors and Anthropic's overload.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestTimeout, http.StatusPaymentRequired,
		http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// retrySame reports whether a retryable failure may clear on the same member a
// moment later (a busy vendor), as opposed to being that member's own state.
func retrySame(o forwardOutcome) bool {
	switch o.status {
	case 0, http.StatusRequestTimeout, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 529:
		return true
	case http.StatusTooManyRequests:
		return o.retryAfter > 0 && o.retryAfter <= maxRetryWait
	}
	return false
}

// retryBackoff is how long to wait before retrying the same member.
func retryBackoff(attempt int, after time.Duration) time.Duration {
	if after > 0 {
		return after
	}
	return retryPause << attempt
}

// retryAfterOf reads a vendor's Retry-After, seconds or HTTP date.
func retryAfterOf(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// drain reads and discards a small amount of a failed response so the
// connection can be reused.
func drain(r io.Reader) { io.CopyN(io.Discard, r, 1<<16) }

// sessionHeaders are where the engines name the conversation a request is part
// of: OpenCode, Pi, Codex and Claude Code.
var sessionHeaders = []string{
	"x-opencode-session", "x-session-affinity", "x-session-id",
	"session_id", "session-id", "x-claude-code-session-id",
}

// conversationKey names the conversation a request belongs to, for affinity.
// It is the engine's session header, else the request body's prompt_cache_key
// (Codex's thread id), else empty (no affinity).
func conversationKey(r *http.Request, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			if len(v) > 128 {
				v = v[:128]
			}
			return "h:" + v
		}
	}
	if vs, ve, ok := findTopLevelValue(body, "prompt_cache_key"); ok && vs < len(body) && body[vs] == '"' {
		var s string
		if json.Unmarshal(body[vs:ve], &s) == nil && s != "" {
			if len(s) > 128 {
				s = s[:128]
			}
			return "k:" + s
		}
	}
	return ""
}

// state is the gateway's in-memory view of member health and use.
type state struct {
	mu     sync.Mutex
	cool   map[string]time.Time // pool\x00member -> cooling until
	fails  map[string]int       // pool\x00member -> consecutive failures
	turns  map[string]int       // pool -> rotate counter
	used   map[string]tokenUse  // pool\x00member -> decayed served count
	series map[string]*minuteSeries
	okN    map[string]int64      // pool\x00member -> requests served
	failN  map[string]int64      // pool\x00member -> failed attempts
	sticks map[string]stickEntry // pool\x00conversation -> member that answered it
	quota  map[string]allowance  // concrete provider -> its last quota reading, for smart
	dirty  bool                  // the conversation table changed since it was saved
}

// gate bounds how many requests run at once; over the limit a caller is
// refused rather than queued.
type gate struct {
	mu    sync.Mutex
	n     int
	limit int
}

func (g *gate) setLimit(n int) {
	g.mu.Lock()
	g.limit = n
	g.mu.Unlock()
}

func (g *gate) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limit > 0 && g.n >= g.limit {
		return false
	}
	g.n++
	return true
}

func (g *gate) leave() {
	g.mu.Lock()
	if g.n > 0 {
		g.n--
	}
	g.mu.Unlock()
}

// lanes bounds how many requests go to each member at once. A caller over the
// limit waits its turn, first first; waiting is not a failure and never moves
// the request to another member.
type lanes struct {
	mu sync.Mutex
	m  map[string]*lane
}

type lane struct {
	limit int
	busy  int
	queue []chan struct{}
}

func newLanes() *lanes { return &lanes{m: map[string]*lane{}} }

// acquire waits for a slot on who's lane, in turn. It answers a release to
// call once the request is done, and false when ctx ended first.
func (l *lanes) acquire(ctx context.Context, who string, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	l.mu.Lock()
	ln := l.m[who]
	if ln == nil {
		ln = &lane{}
		l.m[who] = ln
	}
	ln.limit = limit
	if ln.busy < limit && len(ln.queue) == 0 {
		ln.busy++
		l.mu.Unlock()
		return l.releaser(who, ln), true
	}
	ch := make(chan struct{})
	ln.queue = append(ln.queue, ch)
	l.mu.Unlock()
	select {
	case <-ch:
		return l.releaser(who, ln), true
	case <-ctx.Done():
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, c := range ln.queue {
		if c == ch {
			ln.queue = append(ln.queue[:i], ln.queue[i+1:]...)
			l.drop(who, ln)
			return nil, false
		}
	}
	// granted as ctx ended: the slot is given on to the next
	ln.busy--
	ln.grant()
	l.drop(who, ln)
	return nil, false
}

// releaser gives the slot back, once however often it is called.
func (l *lanes) releaser(who string, ln *lane) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			ln.busy--
			ln.grant()
			l.drop(who, ln)
			l.mu.Unlock()
		})
	}
}

// grant lets those first in the queue go while there is room.
func (ln *lane) grant() {
	for len(ln.queue) > 0 && ln.busy < ln.limit {
		ln.busy++
		close(ln.queue[0])
		ln.queue = ln.queue[1:]
	}
}

// drop forgets a lane nobody holds or waits on.
func (l *lanes) drop(who string, ln *lane) {
	if ln.busy <= 0 && len(ln.queue) == 0 && l.m[who] == ln {
		delete(l.m, who)
	}
}

type tokenUse struct {
	n  float64
	at time.Time
}

type stickEntry struct {
	member string
	at     time.Time
	// cache is how many tokens the last answer read from the vendor's cache
	// or wrote to it, or cacheUnknown when nobody read it.
	cache int64
}

func newState() *state {
	return &state{
		cool:   map[string]time.Time{},
		fails:  map[string]int{},
		turns:  map[string]int{},
		used:   map[string]tokenUse{},
		series: map[string]*minuteSeries{},
		okN:    map[string]int64{},
		failN:  map[string]int64{},
		sticks: map[string]stickEntry{},
		quota:  map[string]allowance{},
	}
}

func key(pool, member string) string { return pool + "\x00" + member }

func (u tokenUse) now(t time.Time) float64 {
	return u.n * math.Exp2(-t.Sub(u.at).Seconds()/halfLife.Seconds())
}

// route is one way to answer a pool's request: a concrete provider, and the
// model to ask it for (empty passes the request's model through).
type route struct {
	member string
	model  string
}

// candidates orders the concrete providers a pool's request may go to: healthy
// ones by the strategy at each level of nesting first — the conversation's own
// answerer ahead of them — then cooling ones by when they come back.
//
// Health, usage and affinity are kept per (pool, concrete provider), for the
// pool the request came to, however deep the provider sits in it.
func (s *state) candidates(cfg *config.Config, pool string, p config.Provider, conv string) []route {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var healthy, cooling []route
	seen := map[string]bool{}
	for _, rt := range s.order(cfg, pool, pool, p, "", now, 0) {
		if seen[rt.member] {
			continue // reached through two sub-pools: the first place counts
		}
		seen[rt.member] = true
		if s.coolingUntil(pool, rt.member, now).IsZero() {
			healthy = append(healthy, rt)
		} else {
			cooling = append(cooling, rt)
		}
	}
	sort.SliceStable(cooling, func(i, j int) bool {
		return s.coolingUntil(pool, cooling[i].member, now).Before(s.coolingUntil(pool, cooling[j].member, now))
	})

	// Affinity: a conversation stays with the member that answered it, so what
	// the vendor cached of it is read again rather than paid for afresh.
	if conv != "" {
		if m, ok := s.stickOf(pool, conv, now); ok {
			for i, h := range healthy {
				if h.member == m {
					ordered := make([]route, 0, len(healthy))
					ordered = append(ordered, h)
					ordered = append(ordered, healthy[:i]...)
					ordered = append(ordered, healthy[i+1:]...)
					healthy = ordered
					break
				}
			}
		}
	}
	return append(healthy, cooling...)
}

// coolingUntil is when a pool's member comes back, or zero when it is not
// cooling. The caller holds the lock.
func (s *state) coolingUntil(pool, member string, now time.Time) time.Time {
	if until, ok := s.cool[key(pool, member)]; ok && until.After(now) {
		return until
	}
	return time.Time{}
}

// order lists the concrete providers under p, reached as name inside the
// request's pool top, by p's strategy; a member that is a pool is expanded by
// its own. A mapping deeper down overrides one above it. The caller holds the
// lock.
func (s *state) order(cfg *config.Config, top, name string, p config.Provider, model string, now time.Time, depth int) []route {
	type entry struct {
		routes  []route
		healthy bool
		back    time.Time // soonest a cooling entry comes back
		used    float64
		rank    smartRank
	}
	var healthy, cooling []entry
	for _, m := range p.Members {
		mp, ok := cfg.Providers[m]
		if !ok {
			continue
		}
		mm := model
		if v := p.MemberModels[m]; v != "" {
			mm = v
		}
		var rs []route
		if mp.IsPool() {
			if depth >= config.MaxPoolDepth {
				continue
			}
			rs = s.order(cfg, top, m, mp, mm, now, depth+1)
		} else {
			rs = []route{{member: m, model: mm}}
		}
		if len(rs) == 0 {
			continue
		}
		e := entry{routes: rs, rank: smartRank{class: rankUnknown}}
		for i, rt := range rs {
			until := s.coolingUntil(top, rt.member, now)
			if until.IsZero() {
				e.healthy = true
			} else if e.back.IsZero() || until.Before(e.back) {
				e.back = until
			}
			e.used += s.used[key(top, rt.member)].now(now)
			if r := s.quota[rt.member].rank(); i == 0 || r.before(e.rank) {
				e.rank = r
			}
		}
		if e.healthy {
			healthy = append(healthy, e)
		} else {
			cooling = append(cooling, e)
		}
	}
	sort.SliceStable(cooling, func(i, j int) bool { return cooling[i].back.Before(cooling[j].back) })

	switch p.StrategyOrDefault() {
	case config.StrategyRotate:
		if n := len(healthy); n > 1 {
			tk := top
			if name != top {
				tk = key(top, name)
			}
			k := s.turns[tk] % n
			s.turns[tk]++
			healthy = append(append([]entry{}, healthy[k:]...), healthy[:k]...)
		}
	case config.StrategyLeastUsed:
		sort.SliceStable(healthy, func(i, j int) bool { return healthy[i].used < healthy[j].used })
	case config.StrategySmart:
		sort.SliceStable(healthy, func(i, j int) bool { return healthy[i].rank.before(healthy[j].rank) })
	}

	var out []route
	for _, e := range append(healthy, cooling...) {
		out = append(out, e.routes...)
	}
	return out
}

// noteCache records how much of the answer on member came from the vendor's
// cache or went into it, which decides whether the conversation is worth
// keeping there.
func (s *state) noteCache(pool, member, conv string, cache int64) {
	if conv == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sticks[key(pool, conv)]
	if !ok || e.member != member {
		return
	}
	e.cache = cache
	s.sticks[key(pool, conv)] = e
	s.dirty = true
}

// stickOf is the member that answered a conversation, if keeping it there is
// still worth what it costs to leave: the vendor has not let go of the prompt,
// and what it cached of it is more than a name. The caller holds the lock.
func (s *state) stickOf(pool, conv string, now time.Time) (string, bool) {
	e, ok := s.sticks[key(pool, conv)]
	if !ok || now.Sub(e.at) > affinityTTL {
		return "", false
	}
	if now.Sub(e.at) > affinityWarm {
		return "", false
	}
	if e.cache != cacheUnknown && e.cache < affinityWorth {
		return "", false
	}
	return e.member, true
}

// evictSticks drops the expired conversations, then the oldest until within
// the bound. The caller holds the lock.
func (s *state) evictSticks(now time.Time) {
	for k, e := range s.sticks {
		if now.Sub(e.at) > affinityTTL {
			delete(s.sticks, k)
		}
	}
	if len(s.sticks) <= affinityMax {
		return
	}
	type kv struct {
		k  string
		at time.Time
	}
	all := make([]kv, 0, len(s.sticks))
	for k, e := range s.sticks {
		all = append(all, kv{k, e.at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for _, x := range all {
		if len(s.sticks) <= affinityMax {
			break
		}
		delete(s.sticks, x.k)
	}
}

func (s *state) succeeded(pool, member, conv string) {
	k := key(pool, member)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cool, k)
	delete(s.fails, k)
	u := s.used[k]
	s.used[k] = tokenUse{n: u.now(now) + 1, at: now}
	s.record(pool, member, true)
	if conv == "" {
		return
	}
	// The cache this answer used is not known yet; noteCache says it once the
	// reply has been read. A different member has none of it either way.
	s.sticks[key(pool, conv)] = stickEntry{member: member, at: now, cache: cacheUnknown}
	s.dirty = true
	if len(s.sticks) > affinityMax {
		s.evictSticks(now)
	}
}

func (s *state) failed(pool, member string, after time.Duration) {
	k := key(pool, member)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[k]++
	s.record(pool, member, false)
	base := baseCooldown << min(s.fails[k]-1, 10)
	if base > maxCooldown {
		base = maxCooldown
	}
	// The vendor's own word of when it is back is kept as given, longer or
	// shorter than ours.
	d := base
	if after > d {
		d = after
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	// Our own backoff gains a fifth of jitter, so members that failed
	// together do not come back together, in one burst at the vendor.
	if d == base {
		d += time.Duration(rand.Int64N(int64(d/5) + 1))
		if d > maxCooldown {
			d = maxCooldown
		}
	}
	s.cool[k] = time.Now().Add(d)
}

// savedStick is one conversation's answerer as the affinity file holds it.
type savedStick struct {
	Pool   string    `json:"pool"`
	Conv   string    `json:"conv"`
	Member string    `json:"member"`
	At     time.Time `json:"at"`
	// Cache is absent for a table written before it was kept, which says
	// nothing either way, and so keeps as an unread one does.
	Cache *int64 `json:"cache,omitempty"`
}

// loadSticks reads the conversation table left by an earlier run, so a gateway
// restart does not hand every session to whoever routing puts first while the
// vendor still has it cached at the one before.
func (s *state) loadSticks(path string) {
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved []savedStick
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range saved {
		if x.Pool == "" || x.Conv == "" || x.Member == "" || now.Sub(x.At) > affinityTTL {
			continue
		}
		cache := int64(cacheUnknown)
		if x.Cache != nil {
			cache = *x.Cache
		}
		s.sticks[key(x.Pool, x.Conv)] = stickEntry{member: x.Member, at: x.At, cache: cache}
	}
	s.evictSticks(now)
}

// saveSticks writes the conversation table when it changed. A problem only
// costs the next run its affinity.
func (s *state) saveSticks(path string) {
	if path == "" {
		return
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.evictSticks(now)
	saved := make([]savedStick, 0, len(s.sticks))
	for k, e := range s.sticks {
		pool, conv, ok := strings.Cut(k, "\x00")
		if !ok {
			continue
		}
		x := savedStick{Pool: pool, Conv: conv, Member: e.member, At: e.at}
		if e.cache != cacheUnknown {
			cache := e.cache
			x.Cache = &cache
		}
		saved = append(saved, x)
	}
	s.dirty = false
	s.mu.Unlock()

	data, err := json.Marshal(saved)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			err = config.AtomicWrite(path, data, 0o600)
		}
	}
	if err != nil {
		// Not written: keep it dirty so the next flush tries again.
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
}
