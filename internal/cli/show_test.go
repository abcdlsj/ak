package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
)

func TestShowProviderPoolRouteAndAccount(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())

	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code", APIKey: "sk-secret", User: "alice", Model: "deepseek/deepseek-v4.1-flash"}
	cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, Account: "goat", ContextWindow: 200000, MaxTokens: 16384}
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, CodexHome: "~/.local/share/ak/codex/cx"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"cl"}, Strategy: config.StrategyOrder}
	cfg.Service.Routes = map[string]config.Route{
		"main": {Members: []string{"goat"}, Strategy: config.StrategySmart},
	}
	cfg.Settings.Default = "cl"
	cfg.Resolve()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	// Plain provider (must mask key/keep account info, show limits and leg).
	outStr, err := runRoot(t, "", "show", "cl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cl", "ak-cl", "claude", "goat", "anthropic", "context 200000", "max_tokens 16384"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("show cl missing %q:\n%s", want, outStr)
		}
	}
	if strings.Contains(outStr, "sk-secret") {
		t.Fatalf("show cl leaked secret:\n%s", outStr)
	}

	// JSON output for pool.
	jsonStr, err := runRoot(t, "", "show", "pool", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep showReport
	if err := json.Unmarshal([]byte(jsonStr), &rep); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, jsonStr)
	}
	if rep.Kind != "pool" || len(rep.Legs) != 1 || rep.Legs[0].Member != "cl" {
		t.Errorf("pool json = %+v", rep)
	}

	// Native provider.
	natStr, err := runRoot(t, "", "show", "cx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(natStr, "native") {
		t.Errorf("show cx missing native:\n%s", natStr)
	}

	// Service route.
	rtStr, err := runRoot(t, "", "show", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rtStr, "route (pool)") || !strings.Contains(rtStr, "goat") || !strings.Contains(rtStr, "smart") {
		t.Errorf("show main:\n%s", rtStr)
	}

	// Account.
	accStr, err := runRoot(t, "", "show", "goat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(accStr, "account") || !strings.Contains(accStr, "alice") || !strings.Contains(accStr, "cl") {
		t.Errorf("show goat:\n%s", accStr)
	}

	// Missing target.
	if _, err := runRoot(t, "", "show", "nope"); err == nil {
		t.Error("show nope succeeded")
	}

	_ = core.Leg{}
}
