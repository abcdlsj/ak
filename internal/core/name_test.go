package core

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestSuggestName(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code"}
	cfg.Accounts["kc"] = config.Account{Vendor: "kimi-coding"}
	cfg.Providers["commandcode-claude"] = config.Provider{Kind: config.KindClaude, Account: "goat"}
	cases := []struct {
		p    config.Provider
		want string
	}{
		{config.Provider{Kind: config.KindCodex, Account: "goat"}, "commandcode-codex"},
		{config.Provider{Kind: config.KindClaude, Account: "goat"}, "commandcode-claude-2"},
		{config.Provider{Kind: config.KindPi, Account: "kc"}, "kimi-pi"},
		{config.Provider{Kind: config.KindPi, PiProvider: "commandcode"}, "commandcode-pi"},
		{config.Provider{Kind: config.KindCodex, BaseURL: "https://cpa.songjian.li/v1"}, "cpa-codex"},
		{config.Provider{Kind: config.KindCodex, BaseURL: "http://api-ai-coding.bilibili.co/api/v1/codex"}, "bilibili-codex"},
		{config.Provider{Kind: config.KindClaude, BaseURL: "http://127.0.0.1:3425"}, ""},
		{config.Provider{Kind: config.KindClaude, Members: []string{"a"}}, ""},
		{config.Provider{Kind: config.KindAntigravity, GeminiDir: "~/.local/share/ak/agy/work"}, "work"},
		{config.Provider{Kind: config.KindCodex, CodexHome: "~/.codex-pro"}, "codex-pro"},
		{config.Provider{Kind: config.KindClaude, ConfigDir: "~/.claude"}, ""},
	}
	for _, c := range cases {
		if got := SuggestName(cfg, c.p); got != c.want {
			t.Errorf("SuggestName(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}
