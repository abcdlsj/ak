package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/vendor"
)

func init() { retryPause = time.Millisecond }

// setRetries fixes how many times a transient failure retries the same member
// for one test.
func setRetries(t *testing.T, n int) {
	t.Helper()
	old := sameMemberRetries
	sameMemberRetries = n
	t.Cleanup(func() { sameMemberRetries = old })
}

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
	setRetries(t, 0)
	bad := newUpstream(t)
	bad.status = http.StatusServiceUnavailable
	good := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: bad.server.URL, APIKey: "sk-a"},
		"b": {Kind: config.KindClaude, BaseURL: good.server.URL, APIKey: "sk-b"},
	}, []string{"a", "b"})

	h := New(cfg).Handler()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	first := bad.hits.Load()
	post(h, "/p/pool/v1/messages", `{"model":"m"}`)
	if got := bad.hits.Load(); got != first {
		t.Errorf("cooling member was tried again: %d -> %d", first, got)
	}
}

func TestExhausted(t *testing.T) {
	bad := newUpstream(t)
	bad.status = http.StatusServiceUnavailable
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: bad.server.URL, APIKey: "sk-a"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"m"}`)
	// The member's own 503 is what the caller sees; a 502 would say nothing
	// of who is out or when it is back.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "a:") {
		t.Errorf("the exhausted body does not name the member: %s", rec.Body)
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

// A member's own model decides what it is asked for; the request's name is
// only what the pool is called, so a pool of differently-set-up members works
// without a --map for each of them.
func TestMemberModel(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a", Model: "member-model"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"client-model"}`)
	if !strings.Contains(rec.Body.String(), `"model":"member-model"`) {
		t.Errorf("the member's model was not used: %s", rec.Body)
	}
}

// A mapping beats the member's own model, which is what a pool over members
// set up for other models needs.
func TestMapBeatsMemberModel(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, map[string]string{"a": "mapped"}, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a", Model: "member-model"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"client-model"}`)
	if !strings.Contains(rec.Body.String(), `"model":"mapped"`) {
		t.Errorf("the mapping was not used: %s", rec.Body)
	}
}

// A member that names no model gets the request's own, so a pool with one
// model name to give clients passes it through untouched.
func TestMemberWithoutModelPassesThrough(t *testing.T) {
	up := newUpstream(t)
	cfg := poolConfig("pool", config.StrategyOrder, nil, map[string]config.Provider{
		"a": {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a"},
	}, []string{"a"})

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"client-model"}`)
	if !strings.Contains(rec.Body.String(), `"model":"client-model"`) {
		t.Errorf("the request's model was not passed through: %s", rec.Body)
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

// A pool's member may be an account: it brings its own endpoint, key and
// model, so a pool over subscriptions needs no provider of its own for each.
func TestPoolOverAccounts(t *testing.T) {
	up := newUpstream(t)
	dir := t.TempDir()
	body := fmt.Sprintf("id = %%q\nname = \"Test\"\n[[endpoints]]\nprotocol = \"anthropic\"\nbase_url = %q\nmodel = \"vendor-model\"\n", up.server.URL)
	if err := os.WriteFile(filepath.Join(dir, "acctvendor.toml"), []byte(fmt.Sprintf(body, "acctvendor")), 0o600); err != nil {
		t.Fatal(err)
	}
	vendor.LoadDir(dir)
	t.Cleanup(func() { vendor.LoadDir("") })

	cfg := &config.Config{
		Version:  config.Version,
		Settings: config.Settings{Prefix: "ak-"},
		Accounts: map[string]config.Account{"acct": {Vendor: "acctvendor", APIKey: "sk-account"}},
		Providers: map[string]config.Provider{
			"pool": {Kind: config.KindClaude, Members: []string{"acct"},
				MemberModels: map[string]string{"acct": "member-model"}},
		},
	}
	cfg.Resolve()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("a pool over an account: %v", err)
	}

	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"asked"}`)
	if !strings.Contains(rec.Body.String(), "member-model") {
		t.Fatalf("body = %s, want the model the pool mapped the account to", rec.Body)
	}
	if got := up.gotAuth.Load(); got != "Bearer sk-account" {
		t.Errorf("auth = %v, want the account's key", got)
	}
	// A member that maps nothing is asked for the model its plan serves.
	delete(cfg.Providers["pool"].MemberModels, "acct")
	p := cfg.Providers["pool"]
	p.MemberModels = nil
	cfg.Providers["pool"] = p
	cfg.Resolve()
	rec = post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"asked"}`)
	if !strings.Contains(rec.Body.String(), "vendor-model") {
		t.Errorf("body = %s, want the account's own model", rec.Body)
	}
}

// A member may name an account and the model to ask it for, as
// <account>/<model>, which is the finest a pool can be pointed at without
// another provider to hang the model on.
func TestPoolMemberNamesAModelOfAnAccount(t *testing.T) {
	up := newUpstream(t)
	dir := t.TempDir()
	spec := "id = \"membervendor\"\nname = \"Test\"\n[[endpoints]]\nprotocol = \"anthropic\"\nbase_url = %q\nmodel = \"vendor-model\"\n"
	if err := os.WriteFile(filepath.Join(dir, "membervendor.toml"), []byte(fmt.Sprintf(spec, up.server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	vendor.LoadDir(dir)
	t.Cleanup(func() { vendor.LoadDir("") })

	cfg := &config.Config{
		Version:  config.Version,
		Settings: config.Settings{Prefix: "ak-"},
		Accounts: map[string]config.Account{"acct": {Vendor: "membervendor", APIKey: "sk-account"}},
		Providers: map[string]config.Provider{
			"pool": {Kind: config.KindClaude, Members: []string{"acct/member/model"}},
		},
	}
	cfg.Resolve()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("a member naming an account's model: %v", err)
	}
	rec := post(New(cfg).Handler(), "/p/pool/v1/messages", `{"model":"asked"}`)
	if !strings.Contains(rec.Body.String(), `"model":"member/model"`) {
		t.Fatalf("body = %s, want the model the member named", rec.Body)
	}

	// The member is not a provider anyone can launch, list, or write.
	if got := cfg.Names(); len(got) != 1 || got[0] != "pool" {
		t.Errorf("names = %v, want the pool alone", got)
	}
}
