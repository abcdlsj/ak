package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/vendor"
)

// remoteConfig is a config whose account "r" is a remote pointing at base.
func remoteConfig(t *testing.T, base, key string) *config.Config {
	t.Helper()
	// A remote is a vendor whose quota source is ak itself; the dir is the
	// process's vendor list, restored when the test ends.
	id := "usagetest-remote"
	dir := t.TempDir()
	toml := "id = \"" + id + "\"\nname = \"test\"\nquota = \"ak\"\nwebsite_url = \"" + base + "\"\n" +
		"[[endpoints]]\nprotocol = \"responses\"\nbase_url = \"" + base + "/v1\"\n"
	if err := os.WriteFile(filepath.Join(dir, id+".toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	vendor.LoadDir(dir)
	t.Cleanup(func() { vendor.LoadDir("") })
	cfg := config.Default()
	cfg.Accounts["r"] = config.Account{Vendor: id, APIKey: key}
	cfg.Providers["r-codex"] = config.Provider{Kind: config.KindCodex, Account: "r", Model: "main"}
	cfg.Providers["local"] = config.Provider{Kind: config.KindCodex, BaseURL: "http://127.0.0.1:1", Model: "main"}
	cfg.Resolve()
	return cfg
}

// The test server stands in for a service: it answers /ak/v1/usage with two
// models under one route, and refuses any other token.
func service(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ak/v1/usage" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"days":7,"rows":[
			{"day":"2026-11-14","route":"main","account":"a","model":"deepseek-v4-pro","input":100,"output":10},
			{"day":"2026-11-14","route":"main","account":"a","model":"deepseek-v4-pro","input":100,"output":10},
			{"day":"2026-11-14","route":"main","account":"b","model":"claude-sonnet-5-5","input":90,"output":9},
			{"day":"2026-11-14","route":"opendesign","account":"b","model":"gpt-6-luna","input":7,"output":0}
		]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchRemoteUsage(t *testing.T) {
	srv := service(t, "tok")
	cfg := remoteConfig(t, srv.URL, "tok")
	served, errs := FetchRemoteUsage(context.Background(), cfg, 7)
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	got := served[RemoteKey{Day: "2026-11-14", Account: "r", Route: "main"}]
	if len(got) != 2 {
		t.Fatalf("main served %d models, want 2: %+v", len(got), got)
	}
	// The two rows of one model are one total, not two entries.
	byModel := map[string]Tokens{}
	for _, s := range got {
		byModel[s.Model] = s.Tokens
	}
	if bm := byModel["deepseek-v4-pro"]; bm.Input != 200 || bm.Output != 20 {
		t.Errorf("deepseek-v4-pro = %+v, want 200 in, 20 out", bm)
	}
	if bm := byModel["claude-sonnet-5-5"]; bm.Input != 90 || bm.Output != 9 {
		t.Errorf("claude-sonnet-5-5 = %+v, want 90 in, 9 out", bm)
	}
	// A second route is its own key, not folded into the first.
	if n := len(served[RemoteKey{Day: "2026-11-14", Account: "r", Route: "opendesign"}]); n != 1 {
		t.Errorf("opendesign served %d models, want 1", n)
	}
}

func TestFetchRemoteUsageRefusesABadToken(t *testing.T) {
	srv := service(t, "tok")
	cfg := remoteConfig(t, srv.URL, "wrong")
	if _, errs := FetchRemoteUsage(context.Background(), cfg, 7); len(errs) != 1 {
		t.Fatalf("errors = %v, want one", errs)
	}
}

// A config with no remote account makes no request at all: the report must
// work offline.
func TestFetchRemoteUsageWithoutRemotes(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["local"] = config.Provider{Kind: config.KindCodex, BaseURL: "http://127.0.0.1:1", Model: "m"}
	served, errs := FetchRemoteUsage(context.Background(), cfg, 7)
	if len(served) != 0 || len(errs) != 0 {
		t.Errorf("served = %v, errors = %v; want nothing", served, errs)
	}
}

func TestRemoteBase(t *testing.T) {
	for in, want := range map[string]string{
		"https://ak.example.com":         "https://ak.example.com",
		"https://ak.example.com/":        "https://ak.example.com",
		"https://ak.example.com/v1":      "https://ak.example.com",
		"https://ak.example.com/ak/v1":   "https://ak.example.com/ak",
		"http://127.0.0.1:8787":          "http://127.0.0.1:8787",
		"https://ak.example.com/v1/":     "https://ak.example.com",
		"https://ak.example.com/v1beta/": "https://ak.example.com/v1beta",
	} {
		got, err := remoteBase(in)
		if err != nil || got != want {
			t.Errorf("remoteBase(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := remoteBase("nope"); err == nil {
		t.Error("a bare word is no service URL")
	}
}

// rows returns one row per (provider, model), as attribution produces them.
func rows(date, provider, model string, in, out int64) Row {
	return Row{Date: date, Provider: provider, Model: model,
		Tokens: Tokens{Input: in, Output: out}, Requests: 1}
}

func TestRefineNamesTheModelBehindARoute(t *testing.T) {
	cfg := remoteConfig(t, "http://127.0.0.1:1", "k")
	served := RemoteUsage{
		RemoteKey{Day: "2026-11-14", Account: "r", Route: "main"}: []Served{{Model: "deepseek-v4-pro", Tokens: Tokens{Input: 100, Output: 10}}},
	}
	got := Refine([]Row{rows("2026-11-14", "r-codex", "main", 40, 4)}, served, cfg)
	if len(got) != 1 || got[0].Model != "deepseek-v4-pro" {
		t.Fatalf("got %+v, want the route renamed to the model that served it", got)
	}
	if got[0].Tokens.Input != 40 || got[0].Tokens.Output != 4 {
		t.Errorf("renaming changed the tokens: %+v", got[0].Tokens)
	}
}

// A route that served two models splits the row in the service's proportions,
// and the parts still add up to the whole.
func TestRefineSplitsARouteThatServedTwoModels(t *testing.T) {
	cfg := remoteConfig(t, "http://127.0.0.1:1", "k")
	served := RemoteUsage{
		RemoteKey{Day: "2026-11-14", Account: "r", Route: "main"}: {
			{Model: "big", Tokens: Tokens{Input: 300, Output: 30}},
			{Model: "small", Tokens: Tokens{Input: 100, Output: 10}},
		},
	}
	got := Refine([]Row{rows("2026-11-14", "r-codex", "main", 400, 40)}, served, cfg)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want one per model", len(got))
	}
	byModel := map[string]Tokens{}
	for _, r := range got {
		if r.Model == "main" {
			t.Errorf("the route name survived: %+v", r)
		}
		byModel[r.Model] = r.Tokens
	}
	if b := byModel["big"]; b.Input != 300 || b.Output != 30 {
		t.Errorf("big = %+v, want 300 in, 30 out", b)
	}
	// Rounding leaves the remainder to the last part, so nothing is lost.
	if s := byModel["small"]; s.Input != 100 || s.Output != 10 {
		t.Errorf("small = %+v, want 100 in, 10 out", s)
	}
}

func TestRefineLeavesWhatItCannotPlace(t *testing.T) {
	cfg := remoteConfig(t, "http://127.0.0.1:1", "k")
	in := []Row{
		rows("2026-11-14", "local", "main", 1, 1),       // not on a remote account
		rows("2026-11-13", "r-codex", "main", 2, 2),     // a day the service did not report
		rows("2026-11-14", "r-codex", "unrouted", 3, 3), // a name the service does not know
	}
	served := RemoteUsage{
		RemoteKey{Day: "2026-11-14", Account: "r", Route: "main"}: []Served{{Model: "deepseek-v4-pro", Tokens: Tokens{Input: 1, Output: 1}}},
	}
	got := Refine(in, served, cfg)
	if len(got) != len(in) {
		t.Fatalf("got %d rows, want %d", len(got), len(in))
	}
	if got[0].Model != "main" || got[1].Model != "main" || got[2].Model != "unrouted" {
		t.Errorf("a row with nowhere to go was rewritten: %+v", got)
	}
}
