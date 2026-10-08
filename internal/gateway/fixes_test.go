package gateway

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

func cooling(s *Server, pool, member string) bool {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	until, ok := s.st.cool[key(pool, member)]
	return ok && until.After(time.Now())
}

// An agent interrupted mid-reply is not the member failing: it is not set
// aside, so the conversation stays where its cache is.
func TestClientCancelDoesNotCoolMember(t *testing.T) {
	setRetries(t, 0)
	started := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(up.Close)
	b := newUpstream(t)
	s := New(twoMemberPool(up.URL, b.server.URL, config.StrategyOrder))
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gw.URL+"/p/pool/v1/messages", strings.NewReader(`{"model":"m","stream":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// The handler finishes on its own goroutine; give it a moment.
	time.Sleep(100 * time.Millisecond)
	if cooling(s, "pool", "a") {
		t.Error("member a was cooled after the client went away")
	}
	if b.hits.Load() != 0 {
		t.Errorf("member b was asked %d times for a request nobody waits on", b.hits.Load())
	}
}

// A member whose key cannot be resolved is that member's problem: the next
// member answers.
func TestUnresolvableKeyFailsOver(t *testing.T) {
	setRetries(t, 0)
	good := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: good.server.URL, APIKeyRef: "env:AK_TEST_UNSET_KEY_FOR_GATEWAY"},
		"b": {Kind: config.KindClaude, BaseURL: good.server.URL, APIKey: "sk-b"},
	}, []string{"a", "b"})
	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if got := good.gotAuth.Load(); got != "Bearer sk-b" {
		t.Errorf("authorization = %v, want Bearer sk-b", got)
	}
}

// pi on the Anthropic API sends x-api-key unless told to send a bearer token.
func TestPiMemberAuthHeader(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mp         config.Provider
		wantKey    string
		wantBearer string
	}{
		{"anthropic default", config.Provider{}, "sk-p", ""},
		{"anthropic bearer", config.Provider{PiAuthHeader: true}, "", "Bearer sk-p"},
		{"openai", config.Provider{PiAPI: "openai-completions"}, "", "Bearer sk-p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream(t)
			mp := tc.mp
			mp.Kind, mp.BaseURL, mp.APIKey = config.KindPi, up.server.URL, "sk-p"
			cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{"a": mp}, []string{"a"})
			pool := cfg.Providers["pool"]
			pool.Kind = config.KindPi
			cfg.Providers["pool"] = pool
			post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
			if got := up.gotKey.Load(); got != tc.wantKey {
				t.Errorf("x-api-key = %v, want %q", got, tc.wantKey)
			}
			if got := up.gotAuth.Load(); got != tc.wantBearer {
				t.Errorf("authorization = %v, want %q", got, tc.wantBearer)
			}
		})
	}
}

// A stream that opens with an error every member would give reaches the agent
// instead of being tried, and cooled, on every member.
func TestStreamOverflowErrorReachesAgent(t *testing.T) {
	setRetries(t, 0)
	a := sseServer(t, sse(`event: error`+"\n"+`data: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`))
	b := newUpstream(t)
	s := New(twoMemberPool(a.URL, b.server.URL, config.StrategyOrder))
	rec := post(s.Handler(), "/p/pool/v1/messages", `{"model":"m","stream":true}`)
	if !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Errorf("body = %q, want the overflow error", rec.Body)
	}
	if b.hits.Load() != 0 {
		t.Errorf("member b was asked %d times", b.hits.Load())
	}
	if cooling(s, "pool", "a") {
		t.Error("member a was cooled for the request's own fault")
	}
}

// A stream that ends cleanly before any known content marker is a complete
// reply; it is passed on, not replayed.
func TestStreamWithoutMarkerIsNotReplayed(t *testing.T) {
	setRetries(t, 0)
	a := sseServer(t, sse(`data: {"type":"ping"}`))
	b := newUpstream(t)
	rec := post(New(twoMemberPool(a.URL, b.server.URL, config.StrategyOrder)).Handler(),
		"/p/pool/v1/messages", `{"model":"m","stream":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ping") {
		t.Errorf("status %d body %q, want a's stream", rec.Code, rec.Body)
	}
	if b.hits.Load() != 0 {
		t.Errorf("member b was asked %d times", b.hits.Load())
	}
}

// A slow plain request is not replayed elsewhere (it would be billed twice);
// a stream that does not open in time is.
func TestHeaderWait(t *testing.T) {
	setRetries(t, 0)
	oldPlain, oldStream := plainHeaderWait, streamHeaderWait
	plainHeaderWait, streamHeaderWait = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { plainHeaderWait, streamHeaderWait = oldPlain, oldStream })

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) }) // runs before slow.Close

	b := newUpstream(t)
	h := New(twoMemberPool(slow.URL, b.server.URL, config.StrategyOrder)).Handler()
	rec := post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("plain: status = %d, want 504", rec.Code)
	}
	if b.hits.Load() != 0 {
		t.Errorf("plain: member b was asked %d times", b.hits.Load())
	}

	b2 := newUpstream(t)
	h = New(twoMemberPool(slow.URL, b2.server.URL, config.StrategyOrder)).Handler()
	rec = post(h, "/p/pool/v1/messages", `{"model":"m","stream":true}`)
	if rec.Code != http.StatusOK || b2.hits.Load() != 1 {
		t.Errorf("stream: status = %d, b hits = %d, want 200 and 1", rec.Code, b2.hits.Load())
	}
}

// A gzipped event stream is decompressed before it is inspected, so an error
// that opens it still fails over.
func TestGzippedStreamIsInspected(t *testing.T) {
	setRetries(t, 0)
	var sawGzip atomic.Bool
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"type":"error","error":{"message":"overloaded"}}`))
			return
		}
		sawGzip.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		io.WriteString(zw, sse(`data: {"type":"error","error":{"message":"overloaded"}}`))
		zw.Close()
	}))
	t.Cleanup(a.Close)
	b := sseServer(t, sse(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"from b"}}`))

	req := httptest.NewRequest(http.MethodPost, "/p/pool/v1/messages", strings.NewReader(`{"model":"m","stream":true}`))
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	New(twoMemberPool(a.URL, b.URL, config.StrategyOrder)).Handler().ServeHTTP(rec, req)
	if !sawGzip.Load() {
		t.Fatal("upstream was not offered gzip")
	}
	if !strings.Contains(rec.Body.String(), "from b") {
		t.Errorf("body = %q, want b's stream", rec.Body)
	}
}
