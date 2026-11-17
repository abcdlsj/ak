package cli

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/usage"
)

// A healthy provider says ok; a broken one names what is wrong.
func TestHealthText(t *testing.T) {
	if got, want := healthText(core.Status{}), out.Mark(out.Fine)+" ok"; got != want {
		t.Errorf("ok = %q, want %q", got, want)
	}
	got := healthText(core.Status{NoKey: true})
	if want := out.Mark(out.Broken) + " no API key"; got != want {
		t.Errorf("no key = %q, want %q", got, want)
	}
}

// A plan is a dim dash when no source serves it, and says why when the read
// failed.
func TestPlanCell(t *testing.T) {
	if got, want := planCell(quota.Quota{NoSource: true}), out.Or(""); got != want {
		t.Errorf("no source = %q, want %q", got, want)
	}
	if got, want := planCell(quota.Quota{Provider: "a"}), out.Or(""); got != want {
		t.Errorf("no reading = %q, want %q", got, want)
	}
	const body = `HTTP 401: {"error":{"message":"Bad key"}}`
	if got, want := planCell(quota.Quota{Error: body}), out.Err("✗ ")+out.Reason(body); got != want {
		t.Errorf("failed read = %q, want %q", got, want)
	}
}

func TestBuildStatusRowsWithAgy(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a.example", APIKey: "k"}
	cfg.Providers["work"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/work"}
	rows := buildStatusRows(
		cfg,
		[]string{"cl", "work"},
		map[string]core.Status{"cl": {}, "work": {NeedsLogin: true}},
		map[string]quota.Quota{"cl": {Source: "deepseek"}, "work": {NoSource: true}},
		map[string]usage.ProviderRow{"cl": {Name: "cl", Tokens: 1200, Cost: 0.42}},
		map[string]usage.AgyActivity{"work": {Provider: "work", Conversations: 3, Steps: 27}},
	)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Quota == nil || rows[0].Tokens != 1200 || !rows[0].OK {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Quota != nil || rows[1].AgyConversations != 3 || rows[1].AgySteps != 27 || rows[1].OK {
		t.Errorf("row 1 = %+v", rows[1])
	}
}
