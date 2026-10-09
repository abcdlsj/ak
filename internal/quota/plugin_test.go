package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// serveFor answers one path with body after check has seen the request.
func serveFor(t *testing.T, path, body string, check func(*http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if check != nil {
			check(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// redirect sends every request to s, keeping the original Host, so a plugin
// with a fixed vendor URL can be tested without a network call.
func redirect(t *testing.T, s *httptest.Server) {
	t.Helper()
	to, _ := url.Parse(s.URL)
	old := client
	client = &http.Client{Transport: rewrite{to}}
	t.Cleanup(func() { client = old })
}

type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// useRegistry loads user plugins from dir for the rest of the test.
func useRegistry(t *testing.T, dir string) registry {
	t.Helper()
	r := loadRegistry(dir)
	regMu.Lock()
	old := reg
	reg = &r
	regMu.Unlock()
	t.Cleanup(func() { regMu.Lock(); reg = old; regMu.Unlock() })
	return r
}

func wantHeader(t *testing.T, r *http.Request, k, v string) {
	t.Helper()
	if got := r.Header.Get(k); got != v {
		t.Errorf("%s = %q, want %q", k, got, v)
	}
}

func wantWindows(t *testing.T, q Quota, want ...Window) {
	t.Helper()
	if len(q.Windows) != len(want) {
		t.Fatalf("windows = %+v, want %d", q.Windows, len(want))
	}
	for i, w := range want {
		g := q.Windows[i]
		if g.Name != w.Name || g.Used != w.Used {
			t.Errorf("window %d = %s %g, want %s %g", i, g.Name, g.Used, w.Name, w.Used)
		}
		if (w.Resets == nil) != (g.Resets == nil) || (w.Resets != nil && !w.Resets.Equal(*g.Resets)) {
			t.Errorf("window %d resets = %v, want %v", i, g.Resets, w.Resets)
		}
	}
}

func at(ms int64) *time.Time { t := time.UnixMilli(ms); return &t }

func TestKimi(t *testing.T) {
	body := `{"user":{"userId":"u1","membership":{"level":"LEVEL_INTERMEDIATE"}},
		"usage":{"limit":"100","used":"26","remaining":"74","resetTime":"2026-10-13T00:00:00Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},
			"detail":{"limit":"100","used":"15","remaining":"85","resetTime":"2026-10-09T12:00:00Z"}}]}`
	s := serveFor(t, "/coding/v1/usages", body, func(r *http.Request) {
		wantHeader(t, r, "Authorization", "Bearer sk-test")
	})
	q := fetch(t, config.Provider{BaseURL: s.URL + "/coding/", Quota: "kimi"})
	five, _ := time.Parse(time.RFC3339, "2026-10-09T12:00:00Z")
	week, _ := time.Parse(time.RFC3339, "2026-10-13T00:00:00Z")
	wantWindows(t, q, Window{Name: "5h", Used: 15, Resets: &five}, Window{Name: "weekly", Used: 26, Resets: &week})
	if q.Kind != "plan" || q.Source != "kimi" {
		t.Errorf("quota = %+v", q)
	}
}

func TestZhipu(t *testing.T) {
	// The weekly limit first: the window is chosen by unit, not by order.
	body := `{"code":200,"msg":"操作成功","success":true,"data":{"level":"pro","limits":[
		{"type":"TIME_LIMIT","unit":5,"number":1,"usage":1000,"currentValue":5,"remaining":995,"percentage":0},
		{"type":"TOKENS_LIMIT","unit":6,"number":7,"percentage":53.0,"nextResetTime":2000000000000},
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":44.0,"nextResetTime":1774967594803}]}}`
	s := serveFor(t, "/api/monitor/usage/quota/limit", body, func(r *http.Request) {
		wantHeader(t, r, "Authorization", "sk-test") // no Bearer
		wantHeader(t, r, "Accept-Language", "en-US,en")
	})
	q := fetch(t, config.Provider{BaseURL: s.URL + "/api/anthropic", Quota: "zhipu"})
	wantWindows(t, q, Window{Name: "5h", Used: 44, Resets: at(1774967594803)}, Window{Name: "weekly", Used: 53, Resets: at(2000000000000)})
	if q.Detail != "pro" {
		t.Errorf("detail = %q", q.Detail)
	}
}

func TestZhipuFailure(t *testing.T) {
	s := serveFor(t, "/api/monitor/usage/quota/limit", `{"code":1001,"msg":"token expired","success":false}`, nil)
	q := Query(context.Background(), "p", config.Provider{BaseURL: s.URL, Quota: "zhipu"}, "k")
	if q.Error != "token expired" {
		t.Fatalf("error = %q", q.Error)
	}
}

func TestMiniMax(t *testing.T) {
	body := `{"model_remains":[
		{"model_name":"video","current_interval_remaining_percent":50.0,"current_weekly_remaining_percent":50.0},
		{"model_name":"general","current_interval_remaining_percent":98.0,"current_weekly_remaining_percent":95.0,
		 "current_interval_status":1,"current_weekly_status":1,"end_time":1780329600000,"weekly_end_time":1780848000000}],
		"base_resp":{"status_code":0,"status_msg":"success"}}`
	for id, host := range map[string]string{"minimax": "api.minimaxi.com", "minimax-intl": "api.minimax.io"} {
		s := serveFor(t, "/v1/api/openplatform/coding_plan/remains", body, func(r *http.Request) {
			if r.Host != host {
				t.Errorf("%s asked %s, want %s", id, r.Host, host)
			}
			wantHeader(t, r, "Authorization", "Bearer sk-test")
		})
		redirect(t, s)
		q := fetch(t, config.Provider{BaseURL: "https://" + host + "/anthropic", Quota: id})
		wantWindows(t, q, Window{Name: "5h", Used: 2, Resets: at(1780329600000)}, Window{Name: "weekly", Used: 5, Resets: at(1780848000000)})
	}
}

func TestMiniMaxNoWeeklyAndError(t *testing.T) {
	// A weekly status of 3 means the plan has no weekly limit.
	s := serveFor(t, "/v1/api/openplatform/coding_plan/remains", `{"model_remains":[{"model_name":"general",
		"current_interval_remaining_percent":80,"current_weekly_status":3,"current_weekly_remaining_percent":100}],
		"base_resp":{"status_code":0}}`, nil)
	redirect(t, s)
	q := fetch(t, config.Provider{BaseURL: "https://api.minimaxi.com/anthropic"})
	wantWindows(t, q, Window{Name: "5h", Used: 20})

	s = serveFor(t, "/v1/api/openplatform/coding_plan/remains", `{"base_resp":{"status_code":1004,"status_msg":"login fail"}}`, nil)
	redirect(t, s)
	q = Query(context.Background(), "p", config.Provider{BaseURL: "https://api.minimax.io"}, "k")
	if q.Error != "login fail" {
		t.Fatalf("error = %q", q.Error)
	}
}

func TestStepFun(t *testing.T) {
	s := serveFor(t, "/v1/accounts", `{"object":"account","balance":"25.6"}`, func(r *http.Request) {
		if r.Host != "api.stepfun.com" {
			t.Errorf("asked %s", r.Host)
		}
		wantHeader(t, r, "Authorization", "Bearer sk-test")
	})
	redirect(t, s)
	q := fetch(t, config.Provider{BaseURL: "https://api.stepfun.ai/v1"})
	if q.Balance == nil || *q.Balance != 25.6 || q.Currency != "CNY" || q.Source != "stepfun" {
		t.Fatalf("quota = %+v", q)
	}
}

func TestNewAPI(t *testing.T) {
	body := `{"success":true,"message":"","data":{"id":7,"username":"me","group":"default","quota":2500000,"used_quota":500000,"request_count":12}}`
	var auth, user string
	s := serveFor(t, "/api/user/self", body, func(r *http.Request) {
		auth, user = r.Header.Get("Authorization"), r.Header.Get("New-Api-User")
	})
	p := config.Provider{BaseURL: s.URL + "/v1", Quota: "newapi",
		Env: map[string]string{"AK_QUOTA_TOKEN": "tok", "AK_QUOTA_USER": "7"}}
	q := fetch(t, p)
	if auth != "Bearer tok" || user != "7" {
		t.Errorf("Authorization = %q, New-Api-User = %q", auth, user)
	}
	if q.Balance == nil || *q.Balance != 5 || q.Used == nil || *q.Used != 1 || q.Currency != "USD" || q.Detail != "default" {
		t.Fatalf("quota = %+v", q)
	}

	// Without a token the key is sent, and an empty user id is left out.
	p.Env = nil
	fetch(t, p)
	if auth != "Bearer sk-test" || user != "" {
		t.Errorf("Authorization = %q, New-Api-User = %q", auth, user)
	}
}

func TestNewAPINotAutoDetected(t *testing.T) {
	if src, err := resolve(config.Provider{BaseURL: "https://relay.example/v1"}); err == nil {
		t.Errorf("a relay host resolved %s", src.ID())
	}
}

func TestExpand(t *testing.T) {
	p := config.Provider{BaseURL: "https://api.example.com/v1/", Env: map[string]string{"TOKEN": "t"}}
	vars := func(n string) string { return requestVar(n, p, "k") }
	cases := map[string]string{
		"{{root}}/x":                     "https://api.example.com/x",
		"{{base}}/x":                     "https://api.example.com/v1/x",
		"Bearer {{ key }}":               "Bearer k",
		"{{env.TOKEN|key}}":              "t",
		"{{env.MISSING|key}}":            "k",
		"{{env.MISSING}}":                "",
		"no template":                    "no template",
		"{{key}}:{{env.TOKEN}}@{{root}}": "k:t@https://api.example.com",
	}
	for in, want := range cases {
		if got := expand(in, vars); got != want {
			t.Errorf("expand(%q) = %q, want %q", in, got, want)
		}
	}
	if err := checkTemplate("{{key}} {{env.X|root}}"); err != nil {
		t.Errorf("a valid template was refused: %v", err)
	}
	for _, bad := range []string{"{{token}}", "{{env.}}", "{{key|nope}}"} {
		if checkTemplate(bad) == nil {
			t.Errorf("checkTemplate(%q) accepted", bad)
		}
	}
}

func TestLookup(t *testing.T) {
	var v any
	dec := json.NewDecoder(strings.NewReader(`{"a":{"list":[{"n":"1.5"},{"n":2}],"nil":null,"s":"x"}}`))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if n, ok := lookupNum(v, "a.list.0.n"); !ok || n != 1.5 {
		t.Errorf("a.list.0.n = %v %v", n, ok)
	}
	if n, ok := lookupNum(v, "a.list.1.n"); !ok || n != 2 {
		t.Errorf("a.list.1.n = %v %v", n, ok)
	}
	for _, missing := range []string{"a.list.2.n", "a.list.-1", "a.list.x", "a.nil", "a.s.deeper", "b"} {
		if _, ok := lookup(v, missing); ok {
			t.Errorf("%s was found", missing)
		}
	}
	if _, ok := lookupNum(v, "a.s"); ok {
		t.Error("a string read as a number")
	}
}

func TestParseTime(t *testing.T) {
	if got := parseTime(json.Number("1700000000")); got == nil || got.Unix() != 1700000000 {
		t.Errorf("seconds = %v", got)
	}
	if got := parseTime(json.Number("1700000000123")); got == nil || got.UnixMilli() != 1700000000123 {
		t.Errorf("millis = %v", got)
	}
	if got := parseTime("1700000000"); got == nil || got.Unix() != 1700000000 {
		t.Errorf("numeric string = %v", got)
	}
	if got := parseTime("2026-10-09T12:00:00.000Z"); got == nil || got.Hour() != 12 {
		t.Errorf("RFC3339 = %v", got)
	}
	for _, none := range []any{json.Number("0"), json.Number("-1"), "soon"} {
		if got := parseTime(none); got != nil {
			t.Errorf("parseTime(%v) = %v", none, got)
		}
	}
}

func TestBuiltinPlugins(t *testing.T) {
	got := strings.Join(ids(builtins), ",")
	for _, id := range []string{"deepseek", "openrouter", "moonshot", "siliconflow", "kimi", "zhipu", "minimax", "minimax-intl", "stepfun", "newapi"} {
		if !strings.Contains(","+got+",", ","+id+",") {
			t.Errorf("built-in %s missing from %s", id, got)
		}
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUserPluginOverrideAndBadFile(t *testing.T) {
	dir := t.TempDir()
	// Overrides the built-in deepseek with another path and shape.
	writeFile(t, dir, "deepseek.toml", `
id = "deepseek"
match = ["deepseek"]
[request]
url = "{{root}}/mine"
headers = { Authorization = "Bearer {{key}}" }
[balance]
value = "left"
currency = "CNY"
`)
	writeFile(t, dir, "myrelay.toml", `
id = "myrelay"
match = ["relay.example"]
[request]
url = "{{root}}/plan"
[[windows]]
name = "day"
used = "u"
limit = "l"
`)
	writeFile(t, dir, "broken.toml", `id = "broken"
[request]
url = "{{root}}/x"
[balance]
valu = "typo"
`)
	writeFile(t, dir, "notes.txt", "ignored")
	r := useRegistry(t, dir)

	if len(r.errs) != 1 || !strings.Contains(r.errs[0].Error(), "broken.toml") || !strings.Contains(r.errs[0].Error(), "balance.valu") {
		t.Fatalf("errs = %v", r.errs)
	}
	if PluginErrors()[0] != r.errs[0] {
		t.Error("PluginErrors does not report the bad file")
	}

	s := serveFor(t, "/mine", `{"left":3}`, nil)
	q := fetch(t, config.Provider{BaseURL: s.URL, Quota: "deepseek"})
	if q.Balance == nil || *q.Balance != 3 {
		t.Fatalf("override not used: %+v", q)
	}

	src, err := resolve(config.Provider{BaseURL: "https://relay.example/v1"})
	if err != nil || src.ID() != "myrelay" {
		t.Fatalf("resolve = %v, %v", src, err)
	}
	if _, err := resolve(config.Provider{BaseURL: "https://x", Quota: "nope"}); err == nil || !strings.Contains(err.Error(), "myrelay") {
		t.Errorf("unknown-id error does not list user plugins: %v", err)
	}
	s = serveFor(t, "/plan", `{"u":30,"l":120}`, nil)
	q = fetch(t, config.Provider{BaseURL: s.URL, Quota: "myrelay"})
	wantWindows(t, q, Window{Name: "day", Used: 25})
}

func TestPluginValidation(t *testing.T) {
	bad := map[string]string{
		"no url":        "id = \"x\"\n[balance]\nvalue = \"v\"",
		"bad id":        "id = \"X Y\"\n[request]\nurl = \"u\"\n[balance]\nvalue = \"v\"",
		"nothing read":  "id = \"x\"\n[request]\nurl = \"u\"",
		"two values":    "id = \"x\"\n[request]\nurl = \"u\"\n[[windows]]\nname = \"w\"\npercent = \"a\"\npercent_left = \"b\"",
		"used no limit": "id = \"x\"\n[request]\nurl = \"u\"\n[[windows]]\nname = \"w\"\nused = \"a\"",
		"where no each": "id = \"x\"\n[request]\nurl = \"u\"\n[[windows]]\nname = \"w\"\npercent = \"a\"\nwhere = { a = 1 }",
		"bad template":  "id = \"x\"\n[request]\nurl = \"{{host}}\"\n[balance]\nvalue = \"v\"",
		"bad kind":      "id = \"x\"\nkind = \"money\"\n[request]\nurl = \"u\"\n[balance]\nvalue = \"v\"",
		"not toml":      "id = ",
	}
	for name, body := range bad {
		if _, err := parsePlugin("f.toml", []byte(body)); err == nil || !strings.HasPrefix(err.Error(), "f.toml: ") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
