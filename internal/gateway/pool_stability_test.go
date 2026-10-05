package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// sse joins server-sent event blocks with the blank line between them.
func sse(events ...string) string {
	return strings.Join(events, "\n\n") + "\n\n"
}

// sseServer answers every request with one fixed event stream.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// serverWith serves every request with one status and body.
func serverWith(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// twoMemberPool builds a pool over a and b with the given strategy.
func twoMemberPool(a, b string, strategy string) *config.Config {
	return poolConfig("pool", strategy, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: a, APIKey: "sk-a"},
		"b": {Kind: config.KindClaude, BaseURL: b, APIKey: "sk-b"},
	}, []string{"a", "b"})
}

// A member whose key is refused or whose path is wrong is that member's
// failure, not the request's: the next one is asked.
func TestFailoverOnMemberAuthAndNotFound(t *testing.T) {
	setRetries(t, 0)
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"forbidden"}}`},
		{"notfound", http.StatusNotFound, `{"error":{"message":"not found"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := serverWith(t, tc.status, tc.body)
			good := newUpstream(t)
			cfg := twoMemberPool(bad.URL, good.server.URL, config.StrategyOrder)
			rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
			}
			if good.hits.Load() != 1 {
				t.Errorf("good member hits = %d, want 1", good.hits.Load())
			}
		})
	}
}

// A 400 that means "this member cannot serve the request" hands it to the next
// member; a 400 that means the request is wrong reaches the agent.
func TestFailoverOnUnservedModelButNotOnBadRequest(t *testing.T) {
	setRetries(t, 0)
	cases := []struct {
		name     string
		body     string
		failover bool
	}{
		{"unserved model", `{"error":{"message":"model \"gpt-6-sol\" is not accessible via the /chat/completions endpoint"}}`, true},
		{"unknown model", `{"error":{"message":"The model gpt-x does not exist"}}`, true},
		{"refused channel", `{"error":{"message":"Illegal API invocation from an unapproved channel"}}`, true},
		{"missing field", `{"error":{"message":"messages: field required"}}`, false},
		{"too long", `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := serverWith(t, http.StatusBadRequest, tc.body)
			good := newUpstream(t)
			cfg := twoMemberPool(bad.URL, good.server.URL, config.StrategyOrder)
			rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
			if tc.failover {
				if rec.Code != http.StatusOK || good.hits.Load() != 1 {
					t.Fatalf("status = %d, good hits = %d (want 200 and 1)", rec.Code, good.hits.Load())
				}
				return
			}
			if rec.Code != http.StatusBadRequest || good.hits.Load() != 0 {
				t.Fatalf("status = %d, good hits = %d (want 400 and 0)", rec.Code, good.hits.Load())
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("the agent did not get the vendor's own error: %s", rec.Body)
			}
		})
	}
}

// A member that fails once and answers next time is retried in place before
// the request is moved on.
func TestSameMemberRetriesOnce(t *testing.T) {
	setRetries(t, 1)
	var hits int
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "busy")
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(flaky.Close)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: flaky.URL, APIKey: "sk-a"},
	}, []string{"a"})
	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if hits != 2 {
		t.Errorf("hits = %d, want 2 (one retry)", hits)
	}
}

// An error that opens a stream, before any content, fails over; the agent
// never sees it.
func TestStreamErrorBeforeContentFailsOver(t *testing.T) {
	setRetries(t, 0)
	overloaded := sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","content":[],"usage":{"input_tokens":3}}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	)
	good := sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m2","role":"assistant","content":[]}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`,
	)
	a := sseServer(t, overloaded)
	b := sseServer(t, good)
	cfg := twoMemberPool(a.URL, b.URL, config.StrategyOrder)
	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "from b") || strings.Contains(rec.Body.String(), "Overloaded") {
		t.Fatalf("the agent got the failed stream instead of the good one: %s", rec.Body)
	}
}

// An error after content has begun is the vendor's reply as it came; the
// agent's retry goes to the next member.
func TestStreamErrorAfterContentPassesThrough(t *testing.T) {
	setRetries(t, 0)
	broken := sse(
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Unable to reach the model provider"}}`,
	)
	a := sseServer(t, broken)
	b := sseServer(t, sse(`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`))
	cfg := twoMemberPool(a.URL, b.URL, config.StrategyOrder)
	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	body := rec.Body.String()
	if !strings.Contains(body, "Let me look") || !strings.Contains(body, "Unable to reach") {
		t.Fatalf("the broken reply was not passed through: %s", body)
	}
	if strings.Contains(body, "from b") {
		t.Fatalf("a stream that failed after its content was retried: %s", body)
	}
}

// rewriteModel changes only the model value; numbers, escaping and key order
// are left exactly as they were.
func TestRewriteModelPreservesEverythingElse(t *testing.T) {
	in := []byte(`{"model":"old","b":1234567890123456789,"s":"a<b&c>d","nested":{"z":1,"a":2}}`)
	want := `{"model":"new","b":1234567890123456789,"s":"a<b&c>d","nested":{"z":1,"a":2}}`
	if got := string(rewriteModel(in, "new")); got != want {
		t.Errorf("rewriteModel corrupted the body:\n got %s\nwant %s", got, want)
	}
	// A nested model field is not the one changed.
	nested := []byte(`{"input":{"model":"inner"},"model":"outer"}`)
	if got := string(rewriteModel(nested, "new")); got != `{"input":{"model":"inner"},"model":"new"}` {
		t.Errorf("rewriteModel changed the wrong model: %s", got)
	}
}

// A conversation stays on the member that answered it, even under rotate.
func TestAffinityKeepsSessionOnAnsweredMember(t *testing.T) {
	a, b := newUpstream(t), newUpstream(t)
	cfg := twoMemberPool(a.server.URL, b.server.URL, config.StrategyRotate)
	h := New(cfg).Handler()
	postSession := func(sid string) {
		req := httptest.NewRequest(http.MethodPost, "/p/pool/v1/messages", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-session-id", sid)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	postSession("s1")
	postSession("s1")
	if a.hits.Load() != 2 || b.hits.Load() != 0 {
		t.Errorf("session did not stay on its answerer: a=%d b=%d", a.hits.Load(), b.hits.Load())
	}
}

// A Codex thread id in the body is a conversation key too.
func TestConversationKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("x-session-id", "abc")
	if k := conversationKey(req, nil); k != "h:abc" {
		t.Errorf("header key = %q, want h:abc", k)
	}
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	if k := conversationKey(req, []byte(`{"prompt_cache_key":"t1","x":1}`)); k != "k:t1" {
		t.Errorf("cache key = %q, want k:t1", k)
	}
}

// A member's own Retry-After is kept as its cooldown when longer.
func TestRetryAfterSetsCooldown(t *testing.T) {
	st := newState()
	st.failed("p", "m", 5*time.Minute)
	d := time.Until(st.cool[key("p", "m")])
	if d < 4*time.Minute || d > 5*time.Minute+time.Second {
		t.Errorf("cooldown = %v, want about 5m", d)
	}
}

// The affinity table stays bounded.
func TestAffinityTableBounded(t *testing.T) {
	st := newState()
	for i := 0; i < affinityMax+50; i++ {
		st.succeeded("p", "m", fmt.Sprintf("conv-%d", i))
	}
	if len(st.sticks) > affinityMax {
		t.Errorf("affinity table = %d entries, want <= %d", len(st.sticks), affinityMax)
	}
}

// A stream that breaks after its content cannot be retried, but the member
// that broke it is set aside for the next request.
func TestStreamBreakCoolsTheMember(t *testing.T) {
	setRetries(t, 0)
	var aHits atomic.Int64
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		aHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me"}}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
	}))
	t.Cleanup(a.Close)
	b := sseServer(t, sse(`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`))
	cfg := twoMemberPool(a.URL, b.URL, config.StrategyOrder)
	h := New(cfg).Handler()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	before := aHits.Load()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	if got := aHits.Load(); got != before {
		t.Errorf("the member that broke the stream was tried again: %d -> %d", before, got)
	}
}
