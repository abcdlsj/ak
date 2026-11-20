package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/vendor"
)

// vendorUp is a fake vendor endpoint that records each request and answers
// with a recorded reply of its protocol.
type vendorUp struct {
	server *httptest.Server
	status int // non-zero: fail with it
	// statusPath fails only these paths, so one endpoint of one vendor can
	// refuse while another answers.
	statusPath map[string]int
	// refusePath fails a path with a status and a body of the vendor's own
	// words, so what it says can be classified as a real answer would be.
	refusePath map[string]pathRefusal

	mu   sync.Mutex
	reqs []seen
}

type seen struct {
	path, auth, key, token, cookie string
	body                           map[string]any
}

// pathRefusal is one path's failure: the status and what the vendor says.
type pathRefusal struct {
	status int
	body   string
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "translate", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newVendorUp(t *testing.T) *vendorUp {
	u := &vendorUp{}
	replies := map[string][2]string{
		"/v1/messages":         {"anthropic_reply.json", "anthropic_stream.sse"},
		"/v1/responses":        {"responses_reply.json", "responses_stream.sse"},
		"/v1/chat/completions": {"chat_reply.json", "chat_stream.sse"},
	}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		u.mu.Lock()
		u.reqs = append(u.reqs, seen{path: r.URL.Path, auth: r.Header.Get("Authorization"),
			key: r.Header.Get("X-Api-Key"), cookie: r.Header.Get("Cookie"), body: body})
		u.mu.Unlock()
		if u.status != 0 {
			w.WriteHeader(u.status)
			io.WriteString(w, `{"error":{"message":"upstream is down"}}`)
			return
		}
		if st := u.statusPath[r.URL.Path]; st != 0 {
			w.WriteHeader(st)
			io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"the plan does not include this model"}}`)
			return
		}
		if ref, ok := u.refusePath[r.URL.Path]; ok {
			w.WriteHeader(ref.status)
			io.WriteString(w, ref.body)
			return
		}
		if r.URL.Path == "/v1/messages/count_tokens" {
			io.WriteString(w, `{"input_tokens":42}`)
			return
		}
		rep, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write(fixture(t, rep[1]))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture(t, rep[0]))
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *vendorUp) last(t *testing.T) seen {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.reqs) == 0 {
		t.Fatal("the upstream was not asked")
	}
	return u.reqs[len(u.reqs)-1]
}

func (u *vendorUp) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.reqs)
}

// countPath is how many requests the upstream saw at one path.
func (u *vendorUp) countPath(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, r := range u.reqs {
		if r.path == path {
			n++
		}
	}
	return n
}

// rig is a service over three vendors of its own: both (claude and codex),
// anth (claude only) and chat (pi on chat completions only), an account on
// each, and a token.
type rig struct {
	srv              *Server
	h                http.Handler
	both, anth, chat *vendorUp
	token            string
}

func newRig(t *testing.T, edit func(*config.Config)) *rig {
	t.Helper()
	t.Setenv(config.DirEnv, t.TempDir())
	r := &rig{both: newVendorUp(t), anth: newVendorUp(t), chat: newVendorUp(t)}
	dir := t.TempDir()
	write := func(id, body string) {
		os.WriteFile(filepath.Join(dir, id+".toml"), []byte(fmt.Sprintf("id = %q\n%s", id, body)), 0o600)
	}
	write("both", fmt.Sprintf("[engines.claude]\nbase_url = %q\nmodel = \"glm-5\"\n[engines.codex]\nbase_url = %q\nmodel = \"gpt-6\"\nwire_api = \"responses\"\n",
		r.both.server.URL, r.both.server.URL+"/v1"))
	write("anth", fmt.Sprintf("[engines.claude]\nbase_url = %q\nmodel = \"kimi-k3\"\nkey_field = \"api_key\"\n", r.anth.server.URL))
	write("chat", fmt.Sprintf("[engines.pi]\nbase_url = %q\nmodel = \"deepseek-v4\"\npi_api = \"openai-completions\"\n", r.chat.server.URL+"/v1"))
	vendor.LoadDir(dir)
	t.Cleanup(func() { vendor.LoadDir(t.TempDir()) })

	tok, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	r.token = tok
	cfg := config.Default()
	cfg.Accounts["bili"] = config.Account{Vendor: "both", APIKey: "key-bili"}
	cfg.Accounts["kimi"] = config.Account{Vendor: "anth", APIKey: "key-kimi"}
	cfg.Accounts["dsk"] = config.Account{Vendor: "chat", APIKey: "key-dsk"}
	cfg.Service.Tokens = map[string]config.Token{"laptop": {Hash: hash}}
	if edit != nil {
		edit(cfg)
	}
	cfg.Resolve()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	r.srv = New(cfg)
	r.srv.Logf = func(string, ...any) {}
	r.h = r.srv.ServiceHandler()
	return r
}

// do sends a request with the rig's token.
func (r *rig) do(t *testing.T, path string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+r.token)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// withModel sets a fixture's model and stream.
func withModel(t *testing.T, name, model string, stream bool) []byte {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(fixture(t, name), &v); err != nil {
		t.Fatal(err)
	}
	v["model"] = model
	if stream {
		v["stream"] = true
	} else {
		delete(v, "stream")
	}
	b, _ := json.Marshal(v)
	return b
}

// A redirect must not carry the vendor's key to another host, and Go does not
// treat x-api-key as sensitive the way it treats Authorization.
func TestSameHostRefusesAnotherHost(t *testing.T) {
	via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/messages"}}}
	same := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com", Path: "/v1/messages"}}
	if err := sameHost(same, via); err != nil {
		t.Errorf("a redirect on the same host: %v", err)
	}
	elsewhere := &http.Request{URL: &url.URL{Scheme: "https", Host: "elsewhere.example.com", Path: "/v1/messages"}}
	if err := sameHost(elsewhere, via); err == nil {
		t.Error("a redirect to another host was followed")
	}
}

func TestServiceNative(t *testing.T) {
	r := newRig(t, nil)

	rec := r.do(t, "/v1/messages?beta=true", withModel(t, "claude_request.json", "bili/glm-5.1", false), "Cookie", "ak_session=x")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := r.both.last(t)
	if got.path != "/v1/messages" || got.body["model"] != "glm-5.1" {
		t.Errorf("upstream got %s model %v", got.path, got.body["model"])
	}
	if got.auth != "Bearer key-bili" || got.cookie != "" || strings.Contains(got.auth, r.token) {
		t.Errorf("upstream auth %q cookie %q: want the account's key and no client credentials", got.auth, got.cookie)
	}
	if !bytes.Equal(rec.Body.Bytes(), fixture(t, "anthropic_reply.json")) {
		t.Errorf("a native reply was changed:\n%s", rec.Body)
	}

	// The account alone is its vendor's default for the protocol asked.
	rec = r.do(t, "/v1/responses", withModel(t, "codex_request.json", "bili", true))
	if got := r.both.last(t); rec.Code != 200 || got.path != "/v1/responses" || got.body["model"] != "gpt-6" {
		t.Errorf("responses on bili: %d %s %v", rec.Code, got.path, got.body["model"])
	}

	// A key_field api_key endpoint gets the key as x-api-key.
	r.do(t, "/v1/messages", withModel(t, "claude_request.json", "kimi", false))
	if got := r.anth.last(t); got.key != "key-kimi" || got.auth != "" || got.body["model"] != "kimi-k3" {
		t.Errorf("kimi got key %q auth %q model %v", got.key, got.auth, got.body["model"])
	}
}

func TestServiceTranslates(t *testing.T) {
	r := newRig(t, nil)

	// Codex on an account that speaks only Anthropic, streamed.
	rec := r.do(t, "/v1/responses", withModel(t, "codex_request.json", "kimi", true))
	got := r.anth.last(t)
	if rec.Code != 200 || got.path != "/v1/messages" || got.body["model"] != "kimi-k3" || got.body["messages"] == nil {
		t.Fatalf("codex on kimi: %d, upstream %s %v\n%s", rec.Code, got.path, got.body["model"], rec.Body)
	}
	out := rec.Body.String()
	for _, want := range []string{"event: response.output_text.delta", `"name":"Read"`, "event: response.completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the translated stream lacks %s:\n%s", want, out)
		}
	}

	// Claude Code on an account that speaks only chat, not streamed.
	rec = r.do(t, "/v1/messages", withModel(t, "claude_request.json", "dsk/deepseek-v4-pro", false))
	got = r.chat.last(t)
	if rec.Code != 200 || got.path != "/v1/chat/completions" || got.body["model"] != "deepseek-v4-pro" {
		t.Fatalf("claude on dsk: %d, upstream %s %v\n%s", rec.Code, got.path, got.body["model"], rec.Body)
	}
	var msg struct {
		Type    string
		Content []struct{ Type, Name string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil || msg.Type != "message" || len(msg.Content) == 0 {
		t.Errorf("the reply is no Anthropic message: %v\n%s", err, rec.Body)
	}

	// Its usage is counted, under the route asked for and the account.
	rows := r.srv.Usage(1)
	var sum Tokens
	for _, u := range rows {
		if u.Token != "laptop" || u.Failed != 0 {
			t.Errorf("usage row %+v", u)
		}
		sum.Output += u.Output
	}
	if len(rows) != 2 || sum.Output != 90 {
		t.Errorf("usage = %+v, want two rows of 45 output tokens", rows)
	}
}

func TestServiceTranslatedError(t *testing.T) {
	r := newRig(t, nil)
	r.chat.status = http.StatusTooManyRequests
	rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "dsk", false))
	// A rate limit reaches the agent as one, in its own protocol, so it can
	// back off instead of treating a 502 as a broken server.
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Errorf("a rate-limited upstream answered %d %s", rec.Code, rec.Body)
	}
}

// The service keeps what a route's callers waited for, and says so.
func TestServiceLatency(t *testing.T) {
	r := newRig(t, nil)
	r.do(t, "/v1/messages", withModel(t, "claude_request.json", "dsk", false))
	rows := r.srv.Latencies()
	if len(rows) != 1 || rows[0].Route != "dsk" || rows[0].Requests != 1 {
		t.Fatalf("latency rows = %+v", rows)
	}
	if rows[0].First95 <= 0 || rows[0].Total95 < rows[0].First95 {
		t.Errorf("first byte %+v, total %+v", rows[0].First95, rows[0].Total95)
	}
}

func TestServicePoolAndAlias(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Service.Routes = map[string]config.Route{
			"main": {Members: []string{"bili/glm-5", "fast"}},
			"fast": {Model: "kimi"},
		}
	})
	r.both.status = http.StatusInternalServerError
	setRetries(t, 0)
	rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "main", false))
	if rec.Code != 200 || r.both.count() != 1 || r.anth.count() != 1 {
		t.Fatalf("main did not fail over to kimi: %d, bili %d, kimi %d\n%s", rec.Code, r.both.count(), r.anth.count(), rec.Body)
	}
	// The member that failed cools off: the next request goes to kimi first.
	r.do(t, "/v1/messages", withModel(t, "claude_request.json", "main", false))
	if r.both.count() != 1 || r.anth.count() != 2 {
		t.Errorf("the failed member was asked again: bili %d, kimi %d", r.both.count(), r.anth.count())
	}
	if u := r.srv.Usage(1); len(u) != 1 || u[0].Route != "main" || u[0].Account != "kimi" || u[0].Requests != 2 {
		t.Errorf("usage = %+v", u)
	}
}

// A conversation's row in the affinity table carries what the reply said about
// the vendor's cache, which is what decides whether it stays there.
func TestServiceNotesTheCacheOfAConversation(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Service.Routes = map[string]config.Route{"main": {Members: []string{"bili/glm-5"}}}
	})
	if rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "main", false),
		"x-session-affinity", "conv-1"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e, ok := r.srv.sst.sticks[key("main", "h:conv-1")]
	if !ok {
		t.Fatal("the conversation was not recorded")
	}
	if e.member != "bili/glm-5" {
		t.Errorf("member = %q, want bili/glm-5", e.member)
	}
	// The fixture reads 30 tokens from the cache, writes none, and sends 120:
	// a vendor that reports no write is taken to hold the prompt it was sent.
	if e.cache != 150 {
		t.Errorf("cache = %d, want the reply's 30 and its 120 prompt", e.cache)
	}
}

// A reply that read too little of the vendor's cache does not pin the
// conversation: the next request takes routing's next choice.
func TestServiceAffinityLeavesAThinCache(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Service.Routes = map[string]config.Route{
			"spread": {Members: []string{"bili/glm-5", "kimi"}, Strategy: "rotate"},
		}
	})
	body := withModel(t, "claude_request.json", "spread", false)
	for i := 0; i < 2; i++ {
		if rec := r.do(t, "/v1/messages", body, "x-session-affinity", "conv-1"); rec.Code != 200 {
			t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if r.both.count() != 1 || r.anth.count() != 1 {
		t.Errorf("a thin cache still pinned the conversation: bili %d, kimi %d; want one each",
			r.both.count(), r.anth.count())
	}
}

func TestServiceRefusals(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Service.Routes = map[string]config.Route{"fast": {Model: "kimi"}}
		_, h, _ := NewToken()
		c.Service.Tokens["phone"] = config.Token{Hash: h, Routes: []string{"fast"}}
	})
	body := withModel(t, "claude_request.json", "nobody/x", false)
	if rec := r.do(t, "/v1/messages", body); rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found_error") {
		t.Errorf("unknown model: %d %s", rec.Code, rec.Body)
	}
	if rec := r.do(t, "/v1/chat/completions", withModel(t, "chat_request.json", "claude-sonnet-5-5", false)); rec.Code != 404 ||
		!strings.Contains(rec.Body.String(), "/v1/models") {
		t.Errorf("unknown model on chat: %d %s", rec.Code, rec.Body)
	}

	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(withModel(t, "claude_request.json", "kimi", false)))
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}

	// A token limited to fast may use fast, and nothing else.
	limited, hash, _ := NewToken()
	cfg := *r.srv.config()
	cfg.Service.Tokens = map[string]config.Token{"laptop": cfg.Service.Tokens["laptop"], "phone": {Hash: hash, Routes: []string{"fast"}}}
	r.srv.Reload(&cfg)
	own := r.token
	r.token = limited
	if rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "fast", false)); rec.Code != 200 {
		t.Errorf("a limited token on its route: %d %s", rec.Code, rec.Body)
	}
	if rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "kimi", false)); rec.Code != 403 {
		t.Errorf("a limited token outside its routes: %d", rec.Code)
	}

	// A revoked token is refused on the next request.
	r.token = own
	cfg2 := cfg
	cfg2.Service.Tokens = map[string]config.Token{"phone": cfg.Service.Tokens["phone"]}
	r.srv.Reload(&cfg2)
	if rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "kimi", false)); rec.Code != 401 {
		t.Errorf("a revoked token: %d", rec.Code)
	}
}

func TestServiceCountTokens(t *testing.T) {
	r := newRig(t, nil)
	if rec := r.do(t, "/v1/messages/count_tokens", withModel(t, "claude_request.json", "kimi", false)); rec.Code != 200 ||
		r.anth.last(t).path != "/v1/messages/count_tokens" {
		t.Errorf("count on kimi: %d %s", rec.Code, rec.Body)
	}
	if rec := r.do(t, "/v1/messages/count_tokens", withModel(t, "claude_request.json", "dsk", false)); rec.Code != 400 ||
		!strings.Contains(rec.Body.String(), "count tokens") || r.chat.count() != 0 {
		t.Errorf("count on a chat-only account: %d %s", rec.Code, rec.Body)
	}
	if len(r.srv.Usage(1)) != 0 {
		t.Error("a token count was billed")
	}
}

func TestServiceModels(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Service.Routes = map[string]config.Route{"main": {Members: []string{"bili", "dsk"}}}
	})
	rec := r.do(t, "/v1/models", nil)
	var list struct {
		Object string
		Data   []struct {
			ID string
			AK struct{ Protocols map[string]string } `json:"ak"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Object != "list" {
		t.Fatalf("models: %v %s", err, rec.Body)
	}
	got := map[string]map[string]string{}
	for _, m := range list.Data {
		got[m.ID] = m.AK.Protocols
	}
	for _, id := range []string{"main", "bili", "bili/glm-5", "bili/gpt-6", "kimi", "kimi/kimi-k3", "dsk", "dsk/deepseek-v4"} {
		if got[id] == nil {
			t.Errorf("models lack %s: %v", id, got)
		}
	}
	if p := got["kimi"]; p["anthropic"] != "native" || p["responses"] != "translated" || p["chat"] != "translated" {
		t.Errorf("kimi protocols = %v", p)
	}
	if p := got["main"]; p["anthropic"] != "mixed" || p["responses"] != "mixed" {
		t.Errorf("main protocols = %v", p)
	}

	rec = r.do(t, "/v1/models", nil, "Anthropic-Version", "2023-06-01")
	if !strings.Contains(rec.Body.String(), `"type":"model"`) || !strings.Contains(rec.Body.String(), `"has_more":false`) {
		t.Errorf("anthropic models shape: %s", rec.Body)
	}
}

func TestServiceQuotaAndUsage(t *testing.T) {
	r := newRig(t, nil)
	r.srv.queryQuota = func(_ context.Context, name string, p config.Provider, key string) quota.Quota {
		if key == "" {
			t.Errorf("%s asked with no key", name)
		}
		return quota.Quota{Provider: name, Source: "test", Windows: []quota.Window{{Name: "5h", Used: 40}}}
	}
	rec := r.do(t, "/ak/v1/quota", nil)
	var q struct{ Accounts []quota.Quota }
	if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil || len(q.Accounts) != 3 || q.Accounts[0].Provider != "bili" {
		t.Errorf("quota: %v %s", err, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "key-") {
		t.Errorf("a key reached the reply: %s", rec.Body)
	}

	r.do(t, "/v1/messages", withModel(t, "claude_request.json", "kimi", true))
	rec = r.do(t, "/ak/v1/usage?days=7", nil)
	var u struct{ Rows []UsageRow }
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || len(u.Rows) != 1 || u.Rows[0].Input != 120 ||
		u.Rows[0].CacheRead != 30 || u.Rows[0].Output != 45 {
		t.Errorf("usage: %v %s", err, rec.Body)
	}
}

func TestCheckExposure(t *testing.T) {
	cfg := config.Default()
	if CheckExposure("127.0.0.1:8787", cfg) != nil || CheckExposure("localhost:8787", cfg) != nil {
		t.Error("loopback was refused")
	}
	if CheckExposure("0.0.0.0:8787", cfg) == nil || CheckExposure(":8787", cfg) == nil {
		t.Error("a public address with no token was allowed")
	}
	cfg.Service.Tokens = map[string]config.Token{"laptop": {Hash: HashToken("x")}}
	if CheckExposure("0.0.0.0:8787", cfg) != nil {
		t.Error("a public address with a token was refused")
	}
}

func TestMeterKeepsUsage(t *testing.T) {
	dir := t.TempDir()
	m := newMeter(dir)
	m.add(UsageRow{Day: "2099-01-01", Token: "laptop", Route: "main", Account: "kimi", Requests: 1, Tokens: Tokens{Input: 3}})
	m.add(UsageRow{Day: "2099-01-01", Token: "laptop", Route: "main", Account: "kimi", Requests: 1, Tokens: Tokens{Input: 4}})
	m.save()
	n := newMeter(dir)
	n.load()
	if rows := n.since(usageDays, ""); len(rows) != 1 || rows[0].Requests != 2 || rows[0].Input != 7 {
		t.Errorf("rows = %+v", rows)
	}
}

// An account that refuses a model on one protocol is asked on the one it does
// serve, and the refusal is remembered: the next request skips the refusal
// instead of asking again.
func TestServiceSkipsARefusedProtocol(t *testing.T) {
	r := newRig(t, nil)
	r.both.statusPath = map[string]int{"/v1/messages": http.StatusForbidden}
	body := withModel(t, "claude_request.json", "bili", false)
	if rec := r.do(t, "/v1/messages", body); rec.Code != 200 {
		t.Fatalf("the response endpoint did not answer: %d %s", rec.Code, rec.Body)
	}
	if n := r.both.countPath("/v1/messages"); n != 1 {
		t.Errorf("the refused endpoint was asked %d times, want 1", n)
	}
	if n := r.both.countPath("/v1/responses"); n != 1 {
		t.Errorf("the response endpoint was asked %d times, want 1", n)
	}
	// The refusal is on record, so the model name says so and the next request
	// goes straight to the protocol that works.
	info := r.srv.info(r.srv.config(), "bili")
	if got := info.Protocols["responses"]; got != "native" {
		t.Errorf("responses = %q, want native", got)
	}
	if len(info.Refused) != 1 || info.Refused[0].Protocol != "anthropic" {
		t.Errorf("refused = %+v, want the anthropic endpoint", info.Refused)
	}
	if rec := r.do(t, "/v1/messages", body); rec.Code != 200 {
		t.Fatalf("the second request: %d %s", rec.Code, rec.Body)
	}
	if n := r.both.countPath("/v1/messages"); n != 1 {
		t.Errorf("the refused endpoint was asked again: %d times", n)
	}
}

// A vendor that says this endpoint does not speak this model has the model on
// another protocol; that is a capability, so it is remembered and the next
// request skips the round trip that would say it again.
func TestServiceRemembersAnEndpointThatCannotSpeakTheModel(t *testing.T) {
	r := newRig(t, nil)
	r.both.refusePath = map[string]pathRefusal{
		"/v1/messages": {status: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Model \"deepseek/deepseek-v4.1-flash\" is not supported on this endpoint. Use /provider/v1/chat/completions for OpenAI and OSS models"}}`},
	}
	body := withModel(t, "claude_request.json", "bili", false)
	if rec := r.do(t, "/v1/messages", body); rec.Code != 200 {
		t.Fatalf("the endpoint that speaks it did not answer: %d %s", rec.Code, rec.Body)
	}
	if n := r.both.countPath("/v1/messages"); n != 1 {
		t.Errorf("the endpoint was asked %d times, want 1", n)
	}
	info := r.srv.info(r.srv.config(), "bili")
	if len(info.Refused) != 1 || info.Refused[0].Protocol != "anthropic" || info.Refused[0].Reason != RefusalProtocol {
		t.Fatalf("refused = %+v, want the anthropic endpoint as a protocol refusal", info.Refused)
	}
	if rec := r.do(t, "/v1/messages", body); rec.Code != 200 {
		t.Fatalf("the second request: %d %s", rec.Code, rec.Body)
	}
	if n := r.both.countPath("/v1/messages"); n != 1 {
		t.Errorf("the refused endpoint was asked again: %d times", n)
	}
}

// A model every protocol refuses is answered with what the vendor said, not
// with a gateway error that hides it.
func TestServiceAnswersARefusal(t *testing.T) {
	r := newRig(t, nil)
	r.both.statusPath = map[string]int{
		"/v1/messages":         http.StatusForbidden,
		"/v1/responses":        http.StatusForbidden,
		"/v1/chat/completions": http.StatusForbidden,
	}
	rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "bili/glm-5", false))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a refused model answered %d, want 403: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "refuses") {
		t.Errorf("the answer does not say what was refused: %s", rec.Body)
	}
}

// A name that asks for one model says only what was said of that model.
func TestServiceRefusedForOneModel(t *testing.T) {
	r := newRig(t, nil)
	r.both.statusPath = map[string]int{"/v1/messages": http.StatusForbidden}
	// glm-5 is what the claude endpoint serves; the refusal is remembered
	// against it.
	r.do(t, "/v1/messages", withModel(t, "claude_request.json", "bili", false))
	if got := r.srv.info(r.srv.config(), "bili").Refused; len(got) != 1 {
		t.Fatalf("the account's refusals = %+v", got)
	}
	if got := r.srv.info(r.srv.config(), "bili/kimi-k3").Refused; len(got) != 0 {
		t.Errorf("a name for another model carried a refusal: %+v", got)
	}
}

// An account whose plan serves one model answers with that model on every
// protocol, so a route over such accounts reports the models it will really
// use rather than the vendor's defaults.
func TestServiceAccountModel(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Accounts["bili"] = config.Account{Vendor: "both", APIKey: "key-bili", Model: "plan-model"}
		c.Service.Routes = map[string]config.Route{"main": {Members: []string{"bili", "dsk"}}}
	})
	rec := r.do(t, "/v1/chat/completions", withModel(t, "chat_request.json", "main", false))
	if rec.Code != 200 || r.both.last(t).body["model"] != "plan-model" {
		t.Errorf("model sent upstream = %v (%d)", r.both.last(t).body["model"], rec.Code)
	}
	// The route reports the model the plan serves, not the vendor's default.
	if m := r.srv.modelsOf(r.srv.config(), "main"); len(m) != 2 || m[0] != "deepseek-v4" || m[1] != "plan-model" {
		t.Errorf("models of main = %v, want the account's model and the other's", m)
	}
}

// A route may name a provider pool instead of repeating its members, so one
// pool answers both as its own command and as a model name: it reports what
// the members answer with, and its members' own models are what they are
// asked for.
func TestServiceRouteOverAProviderPool(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Providers["both-claude"] = config.Provider{Kind: config.KindClaude, Account: "bili"}
		c.Providers["kimi-claude"] = config.Provider{Kind: config.KindClaude, Account: "kimi"}
		c.Providers["mypool"] = config.Provider{Kind: config.KindClaude,
			Members: []string{"both-claude", "kimi-claude"}, Strategy: config.StrategyOrder}
		c.Service.Routes = map[string]config.Route{"dpsk": {Provider: "mypool"}}
	})
	// The route takes the provider's members, and nothing else.
	route := r.srv.config().Service.Routes["dpsk"]
	if got := strings.Join(route.Members, ","); got != "bili/glm-5,kimi/kimi-k3" {
		t.Fatalf("route members = %q, want the pool's accounts and models", got)
	}
	if m := r.srv.modelsOf(r.srv.config(), "dpsk"); len(m) != 2 || m[0] != "glm-5" || m[1] != "kimi-k3" {
		t.Errorf("models of dpsk = %v", m)
	}
	rec := r.do(t, "/v1/messages", withModel(t, "claude_request.json", "dpsk", false))
	if rec.Code != 200 {
		t.Fatalf("dpsk: %d %s", rec.Code, rec.Body)
	}
	// The first member answered, asked for its own model rather than the
	// route's name.
	if r.both.count() != 1 || r.both.last(t).body["model"] != "glm-5" {
		t.Errorf("member was asked for %v (%d requests)", r.both.last(t).body["model"], r.both.count())
	}
}

func TestServiceRouteReachAndRefusals(t *testing.T) {
	r := newRig(t, func(c *config.Config) {
		c.Service.Routes = map[string]config.Route{
			"kimiroute": {Model: "kimi"},
			"mix":       {Members: []string{"bili", "dsk"}},
		}
	})
	kimiReach := r.srv.reach(r.srv.config(), "kimiroute")
	if kimiReach["anthropic"] != "native" || kimiReach["responses"] != "translated" || kimiReach["chat"] != "translated" {
		t.Errorf("kimiroute reach = %v, want anthropic=native, responses/chat=translated", kimiReach)
	}
	mixReach := r.srv.reach(r.srv.config(), "mix")
	if mixReach["anthropic"] != "mixed" || mixReach["responses"] != "mixed" || mixReach["chat"] != "mixed" {
		t.Errorf("mix reach = %v, want mixed across protocols", mixReach)
	}

	r.both.statusPath = map[string]int{"/v1/messages": http.StatusForbidden}
	r.do(t, "/v1/messages", withModel(t, "claude_request.json", "bili", false))
	rows := r.srv.ref.of("bili")
	if len(rows) != 1 || rows[0].Model != "glm-5" || rows[0].Protocol != "anthropic" || rows[0].Reason != RefusalPermission {
		t.Fatalf("refusals of bili = %+v, want glm-5/anthropic/%s", rows, RefusalPermission)
	}
	r.srv.ref.forgetAccount("bili")
	if got := r.srv.ref.of("bili"); len(got) != 0 {
		t.Errorf("refusals after forgetting = %+v, want empty", got)
	}
}
