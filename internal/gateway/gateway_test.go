package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// upstream is a fake provider that records what it received.
type upstream struct {
	server *httptest.Server
	hits   atomic.Int64
	// status, when non-zero, is returned instead of 200.
	status int
	// gotAuth is the Authorization header of the last request.
	gotAuth atomic.Value // string
	// gotKey is the X-Api-Key header of the last request.
	gotKey atomic.Value // string
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.gotAuth.Store(r.Header.Get("Authorization"))
		u.gotKey.Store(r.Header.Get("X-Api-Key"))
		if u.status != 0 {
			w.WriteHeader(u.status)
			io.WriteString(w, "upstream error")
			return
		}
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(u.server.Close)
	return u
}

// poolConfig builds a config with the given members and a single pool.
func poolConfig(name string, strategy string, memberModels map[string]string, members map[string]config.Provider, order []string) *config.Config {
	return &config.Config{
		Version:   config.Version,
		Settings:  config.Settings{Prefix: "ak-"},
		Providers: mergePool(name, strategy, memberModels, members, order),
	}
}

func mergePool(name, strategy string, memberModels map[string]string, members map[string]config.Provider, order []string) map[string]config.Provider {
	out := map[string]config.Provider{}
	for k, v := range members {
		out[k] = v
	}
	out[name] = config.Provider{Kind: config.KindClaude, Members: order, MemberModels: memberModels, Strategy: strategy}
	return out
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ak-pool")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPassthrough(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"claude-opus-4","x":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if got := up.gotAuth.Load(); got != "Bearer sk-a" {
		t.Errorf("upstream auth = %v, want the member key", got)
	}
	if !strings.Contains(rec.Body.String(), `"model":"claude-opus-4"`) {
		t.Errorf("model was not passed through: %s", rec.Body)
	}
}

func TestFailover(t *testing.T) {
	bad := newUpstream(t)
	bad.status = http.StatusServiceUnavailable
	good := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: bad.server.URL, APIKey: "sk-a"},
		"b": {Kind: config.KindClaude, BaseURL: good.server.URL, APIKey: "sk-b"},
	}, []string{"a", "b"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if good.hits.Load() != 1 {
		t.Errorf("good member hits = %d, want 1", good.hits.Load())
	}
}

func TestFailoverCoolsTheFailedMember(t *testing.T) {
	bad := newUpstream(t)
	bad.status = http.StatusServiceUnavailable
	good := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: bad.server.URL, APIKey: "sk-a"},
		"b": {Kind: config.KindClaude, BaseURL: good.server.URL, APIKey: "sk-b"},
	}, []string{"a", "b"})

	h := New(cfg).Handler()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	if got := bad.hits.Load(); got != 1 {
		t.Errorf("cooling member hits = %d, want 1 (tried once, then skipped)", got)
	}
}

func TestExhausted(t *testing.T) {
	bad := newUpstream(t)
	bad.status = http.StatusServiceUnavailable
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: bad.server.URL, APIKey: "sk-a"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestModelMapping(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, map[string]string{"a": "upstream-model"}, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"logical"}`)
	if !strings.Contains(rec.Body.String(), `"model":"upstream-model"`) {
		t.Errorf("mapping not applied: %s", rec.Body)
	}
}

func TestRotate(t *testing.T) {
	a, b := newUpstream(t), newUpstream(t)
	cfg := poolConfig("pool", config.StrategyRotate, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: a.server.URL, APIKey: "sk-a"},
		"b": {Kind: config.KindClaude, BaseURL: b.server.URL, APIKey: "sk-b"},
	}, []string{"a", "b"})

	h := New(cfg).Handler()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	if a.hits.Load() != 1 || b.hits.Load() != 1 {
		t.Errorf("rotate hits a=%d b=%d, want 1 and 1", a.hits.Load(), b.hits.Load())
	}
}

func TestClaudeAPIKeyField(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a", KeyField: "api_key"},
	}, []string{"a"})

	post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	if got := up.gotKey.Load(); got != "sk-a" {
		t.Errorf("x-api-key = %v, want sk-a", got)
	}
	if got := up.gotAuth.Load(); got != "" {
		t.Errorf("authorization = %v, want empty", got)
	}
}

func TestCodexMemberUsesBearer(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a"},
	}, []string{"a"})
	// Sanity: the same path works for a codex-kind member.
	cfg.Providers["a"] = config.Provider{Kind: config.KindCodex, BaseURL: up.server.URL, APIKey: "sk-c"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindCodex, Members: []string{"a"}}
	post(New(cfg).Handler(), "/p/pool/responses", `{"model":"m"}`)
	if got := up.gotAuth.Load(); got != "Bearer sk-c" {
		t.Errorf("authorization = %v, want Bearer sk-c", got)
	}
}

func TestRewriteModelLeavesNonJSONAlone(t *testing.T) {
	in := []byte("not json")
	if got := rewriteModel(in, "x"); string(got) != "not json" {
		t.Errorf("rewriteModel changed non-JSON: %s", got)
	}
}

func TestRewriteModelNoModelField(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"input": "hi"})
	if got := rewriteModel(in, "x"); string(got) != string(in) {
		t.Errorf("rewriteModel changed a body without a model: %s", got)
	}
}
