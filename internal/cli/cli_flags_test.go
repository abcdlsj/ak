package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/usage"
)

func TestUsageFilter(t *testing.T) {
	f, err := usageFilter(0, "2026-10-01", []string{"kimi"})
	if err != nil || f.Since != "2026-10-01" || f.Provider != "kimi" {
		t.Fatalf("got %+v, %v", f, err)
	}
	if f, _ := usageFilter(7, "", nil); f.Since != usage.SinceDays(7) || f.Provider != "" {
		t.Fatalf("days: got %+v", f)
	}
	if _, err := usageFilter(0, "10/01", nil); err == nil {
		t.Fatal("bad --since accepted")
	}
	if _, err := usageFilter(-1, "", nil); err == nil {
		t.Fatal("negative --days accepted")
	}
}

func TestMatchNamesSkipsUsed(t *testing.T) {
	cfg := config.Default()
	for _, n := range []string{"openrouter", "openrouter-claude", "kimi"} {
		cfg.Providers[n] = config.Provider{Kind: config.KindClaude}
	}
	var got []string
	for _, c := range matchNames(cfg, []string{"openrouter"}, "open") {
		name, _, _ := strings.Cut(c, "\t")
		got = append(got, name)
	}
	if !reflect.DeepEqual(got, []string{"openrouter-claude"}) {
		t.Fatalf("got %v", got)
	}
}

// Two providers on one account are asked once; another key is a separate account.
func TestQuotaDedupKey(t *testing.T) {
	a := config.Provider{BaseURL: "https://openrouter.ai/api/v1"}
	b := config.Provider{BaseURL: "https://openrouter.ai/api"}
	if quota.DedupKey(a, "k") != quota.DedupKey(b, "k") {
		t.Fatal("same account keyed apart")
	}
	if quota.DedupKey(a, "k") == quota.DedupKey(a, "k2") {
		t.Fatal("different keys share a query")
	}
}

func TestListEndpointForPiProvider(t *testing.T) {
	if got := endpoint(listRow{PiProvider: "commandcode"}); got != "pi:commandcode" {
		t.Fatalf("got %q", got)
	}
}

func TestDescribeProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", Model: "m"}
	cfg.Providers["p"] = config.Provider{Kind: config.KindClaude, Members: []string{"a"}}
	cfg.Settings.Default = "p"
	if got := describeProvider(cfg, "a"); !strings.HasPrefix(got, "✗ ") || !strings.Contains(got, "no API key") || !strings.HasSuffix(got, "claude · m") {
		t.Errorf("a: %q", got)
	}
	if got := describeProvider(cfg, "p"); !strings.Contains(got, "pool(order: a) · default") {
		t.Errorf("p: %q", got)
	}
}
