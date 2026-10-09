package config

import "testing"

func TestValidateIsolation(t *testing.T) {
	ok := []Provider{
		{Kind: KindClaude, BaseURL: "https://a", APIKey: "k", ConfigDir: "~/.claude-a",
			Settings: map[string]any{"effortLevel": "low", "env": map[string]any{"FOO": "1"}}},
		{Kind: KindCodex, BaseURL: "https://a", APIKey: "k", CodexHome: "~/.codex-a"},
	}
	for i, p := range ok {
		cfg := Default()
		cfg.Providers["a"] = p
		if err := Validate(cfg); err != nil {
			t.Errorf("ok[%d] rejected: %v", i, err)
		}
	}
	bad := map[string]Provider{
		"config_dir on codex":  {Kind: KindCodex, BaseURL: "https://a", APIKey: "k", ConfigDir: "/x"},
		"codex_home on claude": {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", CodexHome: "/x"},
		"settings on pi":       {Kind: KindPi, BaseURL: "https://a", APIKey: "k", Model: "m", Settings: map[string]any{"a": 1}},
		"routing key in layer": {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", Settings: map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://b"}}},
		"auth token in layer":  {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", Settings: map[string]any{"env": map[string]any{"ANTHROPIC_AUTH_TOKEN": "x"}}},
	}
	for name, p := range bad {
		cfg := Default()
		cfg.Providers["a"] = p
		if err := Validate(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
