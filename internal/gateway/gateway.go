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
	"regexp"
	"sort"
	"strconv"
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

// Bounds on a proxied request and its streams.
const (
	// maxBodyBytes is the largest request body buffered: failover replays it,
	// so it is held in memory and capped.
	maxBodyBytes = 128 << 20
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

// Affinity bounds: how long a conversation's answerer is remembered, and how
// many conversations are kept.
const (
	affinityTTL = 24 * time.Hour
	affinityMax = 512
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
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/p/", s.handlePool)
	return mux
}

// Serve listens on addr and serves until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
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
	body, ok := readBody(w, r)
	if !ok {
		return
	}

	conv := conversationKey(r, body)
	members := s.st.candidates(name, p, conv)
	if len(members) == 0 {
		http.Error(w, "ak: pool "+name+" has no members", http.StatusServiceUnavailable)
		return
	}

	var lastErr string
	for i, m := range members {
		mp := s.cfg.Providers[m]
		var res forwardOutcome
		for attempt := 0; ; attempt++ {
			res = s.forward(w, r, name, m, tail, mp, body)
			if res.handled {
				if res.ok {
					s.st.succeeded(name, m, conv)
				} else if res.err != nil {
					// A reply that broke after it began: it cannot be retried, but
					// the member that cut it is set aside for the next request.
					s.st.failed(name, m, 0)
				}
				return
			}
			if !res.retryable {
				break
			}
			// A busy vendor often answers the same member a moment later; try
			// it again before giving up on it.
			if attempt < sameMemberRetries && retrySame(res) {
				wait := retryBackoff(attempt, res.retryAfter)
				if wait <= maxRetryWait {
					s.logf("pool %s: member %s failed (%s); retrying in %s", name, m, res.err, wait)
					select {
					case <-time.After(wait):
						continue
					case <-r.Context().Done():
						return
					}
				}
			}
			break
		}
		if res.err == nil {
			res.err = errors.New("no response")
		}
		if !res.retryable {
			// Nothing was written and no other member can help (e.g. an
			// unresolvable key): tell the caller instead of an empty 200.
			http.Error(w, "ak: pool "+name+": "+res.err.Error(), http.StatusBadGateway)
			return
		}
		lastErr = res.err.Error()
		s.st.failed(name, m, res.retryAfter)
		s.logf("pool %s: member %s failed (%s); trying next", name, m, lastErr)
		if i == len(members)-1 {
			break
		}
	}
	// Every member failed before any bytes were written.
	http.Error(w, "ak: pool "+name+" exhausted its members: "+lastErr, http.StatusBadGateway)
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

// forward sends the request to one member.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, pool, member, tail string, mp config.Provider, body []byte) forwardOutcome {
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
		return forwardOutcome{retryable: true, err: err}
	}
	copyHeaders(req.Header, r.Header)
	key, err := s.res.Resolve(mp)
	if err != nil {
		return forwardOutcome{err: fmt.Errorf("key for member %s: %w", member, err)}
	}
	setAuth(req.Header, mp, key)

	resp, err := s.client.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return forwardOutcome{err: r.Context().Err()} // the client went away
		}
		return forwardOutcome{retryable: true, err: err}
	}
	defer resp.Body.Close()

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

// mpModel is the model to ask the member for: the pool's mapping for it, else
// empty meaning pass the request's model through.
func mpModel(member string, pool config.Provider) string {
	if pool.MemberModels == nil {
		return ""
	}
	return pool.MemberModels[member]
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
					return forwardOutcome{handled: true, err: werr}
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
					w.WriteHeader(resp.StatusCode)
					if _, werr := w.Write(head); werr != nil {
						return forwardOutcome{handled: true, err: werr}
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
	if v.Type == "error" || v.Object == "error" {
		return false, true
	}
	if len(v.Error) > 0 && !bytes.Equal(v.Error, []byte("null")) {
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
				return werr
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
}

type tokenUse struct {
	n  float64
	at time.Time
}

type stickEntry struct {
	member string
	at     time.Time
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
	}
}

func key(pool, member string) string { return pool + "\x00" + member }

func (u tokenUse) now(t time.Time) float64 {
	return u.n * math.Exp2(-t.Sub(u.at).Seconds()/halfLife.Seconds())
}

// candidates orders a pool's members for one request: healthy members by the
// strategy first — the conversation's own answerer ahead of them — then cooling
// ones by when they come back.
func (s *state) candidates(pool string, p config.Provider, conv string) []string {
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

	// Affinity: a conversation stays with the member that answered it, so what
	// the vendor cached of it is read again rather than paid for afresh.
	if conv != "" {
		if m, ok := s.stickOf(pool, conv, now); ok {
			for i, h := range healthy {
				if h == m {
					ordered := make([]string, 0, len(healthy))
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

// stickOf is the member that answered a conversation, if it is still fresh.
// The caller holds the lock.
func (s *state) stickOf(pool, conv string, now time.Time) (string, bool) {
	e, ok := s.sticks[key(pool, conv)]
	if !ok || now.Sub(e.at) > affinityTTL {
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
	s.sticks[key(pool, conv)] = stickEntry{member: member, at: now}
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
	d := baseCooldown << min(s.fails[k]-1, 10)
	if d > maxCooldown {
		d = maxCooldown
	}
	// The vendor's own word of when it is back is kept when longer.
	if after > d {
		d = after
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	s.cool[k] = time.Now().Add(d)
}
