package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/probe"
	"github.com/abcdlsj/ak/internal/secrets"
)

func TestRunChecksPool(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(up.Close)

	cfg := config.Default()
	cfg.Providers["good"] = config.Provider{Kind: config.KindClaude, BaseURL: up.URL, APIKey: "good", Model: "m"}
	cfg.Providers["bad"] = config.Provider{Kind: config.KindClaude, BaseURL: up.URL, APIKey: "bad", Model: "m"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"bad", "good"}}

	results, err := runChecks(context.Background(), cfg, []string{"pool"}, secrets.Default())
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name   string
		status probe.Status
	}{{"bad", probe.StatusAuth}, {"good", probe.StatusOK}, {"pool", probe.StatusOK}}
	if len(results) != len(want) {
		t.Fatalf("results = %+v", results)
	}
	for i, w := range want {
		if results[i].Provider != w.name || results[i].Status != w.status {
			t.Errorf("row %d = %s %s, want %s %s", i, results[i].Provider, results[i].Status, w.name, w.status)
		}
	}
	if n := failedChecks(results); n != 1 {
		t.Fatalf("failed = %d, want 1", n)
	}
}
