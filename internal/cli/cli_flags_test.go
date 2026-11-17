package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
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
	// A directory is an account of its own: two native providers share nothing.
	c := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/a"}
	d := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/b"}
	if quota.DedupKey(c, "") == quota.DedupKey(d, "") {
		t.Fatal("two isolated accounts share a query")
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

// wait returns once any target has allowance, sleeping until the reset the
// vendor gave, and fails at once when nothing can be read.
func TestWaitQuota(t *testing.T) {
	hs := []core.Holder{{Name: "a"}}
	calls := 0
	reset := time.Now().Add(50 * time.Millisecond)
	err := waitQuota(context.Background(), hs, time.Hour, func(_ context.Context, h core.Holder) quota.Quota {
		n := h.Name
		calls++
		if calls == 1 {
			return quota.Quota{Provider: n, Windows: []quota.Window{{Used: 100, Resets: &reset}}}
		}
		return quota.Quota{Provider: n, Windows: []quota.Window{{Used: 10}}}
	})
	if err != nil || calls != 2 {
		t.Fatalf("err %v after %d calls", err, calls)
	}

	err = waitQuota(context.Background(), hs, time.Hour, func(_ context.Context, h core.Holder) quota.Quota {
		n := h.Name
		return quota.Quota{Provider: n, NoSource: true}
	})
	if err == nil {
		t.Fatal("no source waited")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	zero := 0.0
	err = waitQuota(ctx, hs, 10*time.Millisecond, func(_ context.Context, h core.Holder) quota.Quota {
		n := h.Name
		return quota.Quota{Provider: n, Balance: &zero}
	})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestManageContextWindowAndMaxTokensFlags(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())

	if _, err := runRoot(t, "", "add", "c", "--kind", "claude", "--base-url", "https://a.example", "--key", "sk-1", "--context-window", "200000", "--max-tokens", "32000"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Providers["c"]; p.ContextWindow != 200000 || p.MaxTokens != 32000 {
		t.Fatalf("added limits = %+v", p)
	}

	if _, err := runRoot(t, "", "edit", "c", "--max-tokens", "0"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()
	if p := cfg.Providers["c"]; p.ContextWindow != 200000 || p.MaxTokens != 0 {
		t.Fatalf("edited limits = %+v", p)
	}
}
