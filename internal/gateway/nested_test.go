package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
)

func members(rs []route) string {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.member + "=" + r.model
	}
	return strings.Join(names, ",")
}

// A member that is a pool is expanded by its own strategy, and a mapping
// deeper down wins over one above it.
func TestNestedPoolOrderAndMapping(t *testing.T) {
	cfg := &config.Config{Version: config.Version, Settings: config.Settings{Prefix: "ak-"}, Providers: map[string]config.Provider{
		"a":     {Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k"},
		"b":     {Kind: config.KindClaude, BaseURL: "https://b", APIKey: "k"},
		"c":     {Kind: config.KindClaude, BaseURL: "https://c", APIKey: "k"},
		"inner": {Kind: config.KindClaude, Members: []string{"a", "b"}, Strategy: config.StrategyRotate, MemberModels: map[string]string{"b": "mb"}},
		"outer": {Kind: config.KindClaude, Members: []string{"inner", "c"}, MemberModels: map[string]string{"inner": "mi", "c": "mc"}},
	}}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	st := newState()
	first := members(st.candidates(cfg, "outer", cfg.Providers["outer"], ""))
	second := members(st.candidates(cfg, "outer", cfg.Providers["outer"], ""))
	if first != "a=mi,b=mb,c=mc" || second != "b=mb,a=mi,c=mc" {
		t.Fatalf("got %q then %q", first, second)
	}

	// A cooling leaf goes to the back; the sub-pool still leads with the other.
	st.failed("outer", "a", 0)
	if got := members(st.candidates(cfg, "outer", cfg.Providers["outer"], "")); !strings.HasSuffix(got, "a=mi") || !strings.HasPrefix(got, "b=mb") {
		t.Fatalf("cooling: %q", got)
	}
}

// A request through a nested pool reaches the leaf with its mapped model.
func TestNestedPoolForwards(t *testing.T) {
	up := newUpstream(t)
	cfg := &config.Config{Version: config.Version, Settings: config.Settings{Prefix: "ak-"}, Providers: map[string]config.Provider{
		"a":     {Kind: config.KindClaude, BaseURL: up.server.URL, APIKey: "sk-a"},
		"inner": {Kind: config.KindClaude, Members: []string{"a"}, MemberModels: map[string]string{"a": "deep"}},
		"outer": {Kind: config.KindClaude, Members: []string{"inner"}},
	}}
	rec := post(New(cfg).Handler(), "/p/outer/v1/messages", `{"model":"m"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"model":"deep"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := up.gotAuth.Load(); got != "Bearer sk-a" {
		t.Errorf("auth = %v", got)
	}
}

// Smart puts allowance that resets soonest first, then members with nothing
// known or no reset, then spent ones by when they come back.
func TestSmartOrder(t *testing.T) {
	now := time.Now()
	in1, in3, in5 := now.Add(time.Hour), now.Add(3*time.Hour), now.Add(5*time.Hour)
	cfg := &config.Config{Version: config.Version, Providers: map[string]config.Provider{}}
	var order []string
	for _, n := range []string{"unknown", "spent-late", "late", "spent-soon", "soon"} {
		cfg.Providers[n] = config.Provider{Kind: config.KindClaude, BaseURL: "https://" + n, APIKey: n}
		order = append(order, n)
	}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: order, Strategy: config.StrategySmart}

	st := newState()
	st.quota["soon"] = allowance{known: true, next: &in1}
	st.quota["late"] = allowance{known: true, next: &in3}
	st.quota["spent-soon"] = allowance{known: true, spent: true, back: &in3}
	st.quota["spent-late"] = allowance{known: true, spent: true, back: &in5}
	got := members(st.candidates(cfg, "pool", cfg.Providers["pool"], ""))
	if want := "soon=,late=,unknown=,spent-soon=,spent-late="; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The gateway reads smart members' quota once per account.
func TestReadQuotaOncePerAccount(t *testing.T) {
	cfg := &config.Config{Version: config.Version, Providers: map[string]config.Provider{
		"a":     {Kind: config.KindClaude, BaseURL: "https://api.kimi.com/coding", APIKey: "same"},
		"b":     {Kind: config.KindClaude, BaseURL: "https://api.kimi.com/coding/", APIKey: "same"},
		"pool":  {Kind: config.KindClaude, Members: []string{"a", "b"}, Strategy: config.StrategySmart},
		"plain": {Kind: config.KindClaude, Members: []string{"a"}},
	}}
	s := New(cfg)
	asked := 0
	reset := time.Now().Add(time.Hour)
	s.queryQuota = func(_ context.Context, name string, _ config.Provider, _ string) quota.Quota {
		asked++
		return quota.Quota{Provider: name, Windows: []quota.Window{{Name: "5h", Used: 100, Resets: &reset}}}
	}
	s.readQuota(context.Background())
	if asked != 1 {
		t.Fatalf("asked %d times, want 1", asked)
	}
	if a := s.st.quota["b"]; !a.spent || a.back == nil || !a.back.Equal(reset) {
		t.Fatalf("b = %+v", a)
	}
}
