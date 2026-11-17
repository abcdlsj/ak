package config

import "testing"

func TestValidateIsolation(t *testing.T) {
	ok := []Provider{
		{Kind: KindClaude, BaseURL: "https://a", APIKey: "k", ConfigDir: "~/.claude-a",
			Settings: map[string]any{"effortLevel": "low", "env": map[string]any{"FOO": "1"}}},
		{Kind: KindCodex, BaseURL: "https://a", APIKey: "k", CodexHome: "~/.codex-a"},
		{Kind: KindPi, BaseURL: "https://a", Model: "m", APIKey: "k", PiDir: "~/.pi-a"},
		{Kind: KindAntigravity, GeminiDir: "~/.agy-a"},
		// Native OAuth providers authenticate through their own isolated directory.
		{Kind: KindClaude, ConfigDir: "~/.claude-pro"},
		{Kind: KindCodex, CodexHome: "~/.codex-pro"},
		{Kind: KindPi, PiDir: "~/.pi-pro"},
	}
	for i, p := range ok {
		cfg := Default()
		cfg.Providers["a"] = p
		if err := Validate(cfg); err != nil {
			t.Errorf("ok[%d] rejected: %v", i, err)
		}
	}
	bad := map[string]Provider{
		"config_dir on codex":    {Kind: KindCodex, BaseURL: "https://a", APIKey: "k", ConfigDir: "/x"},
		"codex_home on claude":   {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", CodexHome: "/x"},
		"pi_dir on claude":       {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", PiDir: "/x"},
		"pi_dir on codex":        {Kind: KindCodex, BaseURL: "https://a", APIKey: "k", PiDir: "/x"},
		"settings on pi":         {Kind: KindPi, BaseURL: "https://a", APIKey: "k", Model: "m", Settings: map[string]any{"a": 1}},
		"gemini_dir on claude":   {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", GeminiDir: "/x"},
		"config_dir on agy":      {Kind: KindAntigravity, GeminiDir: "/x", ConfigDir: "/y"},
		"codex_home on agy":      {Kind: KindAntigravity, GeminiDir: "/x", CodexHome: "/y"},
		"pi_dir on agy":          {Kind: KindAntigravity, GeminiDir: "/x", PiDir: "/y"},
		"agy without dir":        {Kind: KindAntigravity},
		"agy with base_url":      {Kind: KindAntigravity, GeminiDir: "/x", BaseURL: "https://a"},
		"agy with key":           {Kind: KindAntigravity, GeminiDir: "/x", APIKey: "k"},
		"claude native with key": {Kind: KindClaude, ConfigDir: "/x", APIKey: "k"},
		"codex native with key":  {Kind: KindCodex, CodexHome: "/x", APIKey: "k"},
		"pi native with key":     {Kind: KindPi, PiDir: "/x", APIKey: "k"},
		"agy as pool":            {Kind: KindAntigravity, GeminiDir: "/x", Members: []string{"b"}},
		"agy effort variant":     {Kind: KindAntigravity, GeminiDir: "/x", Variants: map[string]Variant{"high": {Model: "m"}}},
		"routing key in layer":   {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", Settings: map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://b"}}},
		"auth token in layer":    {Kind: KindClaude, BaseURL: "https://a", APIKey: "k", Settings: map[string]any{"env": map[string]any{"ANTHROPIC_AUTH_TOKEN": "x"}}},
	}
	for name, p := range bad {
		cfg := Default()
		cfg.Providers["a"] = p
		if err := Validate(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseKindAndHomeDirHelpers(t *testing.T) {
	if k, ok := ParseKind("agy"); !ok || k != KindAntigravity {
		t.Fatalf("ParseKind(agy) = %q, %v", k, ok)
	}
	if got := KindAntigravity.CommandName(); got != "agy" {
		t.Fatalf("CommandName = %q, want agy", got)
	}
	if _, ok := ParseKind("unknown"); ok {
		t.Fatal("ParseKind(unknown) accepted")
	}
	p := Provider{Kind: KindPi}
	p.SetHomeDir("/tmp/pi-work")
	if p.HomeDir() != "/tmp/pi-work" || p.PiDir != "/tmp/pi-work" || !p.UsesEngineLogin() {
		t.Fatalf("pi native provider = %+v", p)
	}
	// Native OAuth providers cannot join a pool.
	cfg := Default()
	cfg.Providers["native_codex"] = Provider{Kind: KindCodex, CodexHome: "/tmp/cx"}
	cfg.Providers["pool"] = Provider{Kind: KindCodex, Members: []string{"native_codex"}}
	if err := Validate(cfg); err == nil {
		t.Fatal("pool with native codex member was accepted")
	}
}
