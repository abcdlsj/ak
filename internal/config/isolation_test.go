package config

import "testing"

func TestValidateIsolation(t *testing.T) {
	ok := []Provider{
		{Kind: KindClaude, BaseURL: "https://a", APIKey: "k", ConfigDir: "~/.claude-a",
			Settings: map[string]any{"effortLevel": "low", "env": map[string]any{"FOO": "1"}}},
		{Kind: KindCodex, BaseURL: "https://a", APIKey: "k", CodexHome: "~/.codex-a"},
		{Kind: KindAntigravity, GeminiDir: "~/.agy-a"},
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
		"gemini_dir on claude": {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", GeminiDir: "/x"},
		"config_dir on agy":    {Kind: KindAntigravity, GeminiDir: "/x", ConfigDir: "/y"},
		"codex_home on agy":    {Kind: KindAntigravity, GeminiDir: "/x", CodexHome: "/y"},
		"agy without dir":      {Kind: KindAntigravity},
		"agy with base_url":    {Kind: KindAntigravity, GeminiDir: "/x", BaseURL: "https://a"},
		"agy with key":         {Kind: KindAntigravity, GeminiDir: "/x", APIKey: "k"},
		"agy as pool":          {Kind: KindAntigravity, GeminiDir: "/x", Members: []string{"b"}},
		"agy effort variant":   {Kind: KindAntigravity, GeminiDir: "/x", Variants: map[string]Variant{"high": {Model: "m"}}},
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
