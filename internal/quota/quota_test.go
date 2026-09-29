package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// serveJSON answers one path with body, 404 otherwise.
func serveJSON(t *testing.T, path, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("%s: Authorization = %q", path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func fetch(t *testing.T, p config.Provider) Quota {
	t.Helper()
	q := Query(context.Background(), "p", p, "sk-test")
	if q.Error != "" {
		t.Fatalf("Query error: %s", q.Error)
	}
	return q
}

func TestDeepSeek(t *testing.T) {
	s := serveJSON(t, "/user/balance", `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"42.50","granted_balance":"2","topped_up_balance":"40.5"}]}`)
	q := fetch(t, config.Provider{BaseURL: s.URL + "/anthropic", Quota: "deepseek"})
	if q.Balance == nil || *q.Balance != 42.5 || q.Currency != "CNY" {
		t.Fatalf("quota = %+v", q)
	}
}

func TestOpenRouterKeyLimit(t *testing.T) {
	s := serveJSON(t, "/api/v1/key", `{"data":{"label":"mine","usage":3.5,"limit":10,"limit_remaining":6.5}}`)
	q := fetch(t, config.Provider{BaseURL: s.URL + "/v1", Quota: "openrouter"})
	if q.Balance == nil || *q.Balance != 6.5 || q.Limit == nil || *q.Limit != 10 || *q.Used != 3.5 {
		t.Fatalf("quota = %+v", q)
	}
	if q.Detail != "mine" {
		t.Errorf("detail = %q", q.Detail)
	}
}

func TestOpenRouterCreditsFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/key", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"label":"credits","usage":4,"limit":null,"limit_remaining":null}}`))
	})
	mux.HandleFunc("/api/v1/credits", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"total_credits":20,"total_usage":4}}`))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)

	q := fetch(t, config.Provider{BaseURL: s.URL, Quota: "openrouter"})
	if q.Balance == nil || *q.Balance != 16 || *q.Limit != 20 {
		t.Fatalf("quota = %+v", q)
	}
}

func TestMoonshot(t *testing.T) {
	s := serveJSON(t, "/v1/users/me/balance", `{"code":0,"data":{"available_balance":88.8,"voucher_balance":8,"cash_balance":80.8}}`)
	q := fetch(t, config.Provider{BaseURL: s.URL + "/anthropic", Quota: "moonshot"})
	if q.Balance == nil || *q.Balance != 88.8 {
		t.Fatalf("quota = %+v", q)
	}
}

func TestSiliconFlow(t *testing.T) {
	s := serveJSON(t, "/v1/user/info", `{"code":20000,"data":{"balance":"5","totalBalance":"12.5","chargeBalance":"10"}}`)
	q := fetch(t, config.Provider{BaseURL: s.URL + "/v1", Quota: "siliconflow"})
	if q.Balance == nil || *q.Balance != 12.5 {
		t.Fatalf("quota = %+v", q)
	}
}

func TestScriptNumber(t *testing.T) {
	q := fetch(t, config.Provider{QuotaCmd: `printf '7.25'`})
	if q.Balance == nil || *q.Balance != 7.25 || q.Source != "script" {
		t.Fatalf("quota = %+v", q)
	}
}

func TestScriptJSON(t *testing.T) {
	q := fetch(t, config.Provider{QuotaCmd: `printf '%s' '{"kind":"plan","windows":[{"name":"5h","used":40}]}'`})
	if len(q.Windows) != 1 || q.Windows[0].Used != 40 {
		t.Fatalf("quota = %+v", q)
	}
}

func TestScriptBadOutput(t *testing.T) {
	q := Query(context.Background(), "p", config.Provider{QuotaCmd: `printf 'not json'`}, "")
	if q.Error == "" {
		t.Fatal("a non-JSON script output was accepted")
	}
}

func TestAutoDetectByHost(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/anthropic": "deepseek",
		"https://openrouter.ai/api/v1":       "openrouter",
		"https://api.moonshot.cn/anthropic":  "moonshot",
		"https://api.siliconflow.cn/v1":      "siliconflow",
	}
	for base, want := range cases {
		src, err := resolve(config.Provider{BaseURL: base})
		if err != nil {
			t.Errorf("resolve(%s): %v", base, err)
			continue
		}
		if src.ID() != want {
			t.Errorf("resolve(%s) = %s, want %s", base, src.ID(), want)
		}
	}
}

func TestOffAndUnknown(t *testing.T) {
	if _, err := resolve(config.Provider{BaseURL: "https://api.deepseek.com", Quota: "off"}); err == nil {
		t.Error("quota=off still resolved a source")
	}
	if _, err := resolve(config.Provider{BaseURL: "https://x.example", Quota: "nope"}); err == nil {
		t.Error("an unknown quota source resolved")
	}
	if _, err := resolve(config.Provider{BaseURL: "https://x.example"}); err == nil {
		t.Error("an unknown host resolved a source")
	}
}
