package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/out"
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

// A status says what it is with its mark: an answer is fine, a slow one is
// worth a look, a skip is unknown and a failure is broken.
func TestCheckMarks(t *testing.T) {
	cases := map[probe.Status]out.Level{
		probe.StatusOK:    out.Fine,
		probe.StatusSlow:  out.Notice,
		probe.StatusSkip:  out.Unknown,
		probe.StatusAuth:  out.Broken,
		probe.StatusModel: out.Broken,
		probe.StatusFail:  out.Broken,
	}
	for s, want := range cases {
		if got := checkLevel(s); got != want {
			t.Errorf("checkLevel(%s) = %v, want %v", s, got, want)
		}
		if want := out.Mark(want) + " " + string(s); checkText(s) != want {
			t.Errorf("checkText(%s) = %q, want %q", s, checkText(s), want)
		}
	}
}
