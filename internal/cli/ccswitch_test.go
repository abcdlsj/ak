package cli

import (
	"testing"

	"github.com/abcdlsj/ak/internal/ccswitch"
	"github.com/abcdlsj/ak/internal/config"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"OpenRouter KIMI": "openrouter-kimi",
		"bili Claude":     "bili-claude",
		"TiMi CC":         "timi-cc",
		"StepFun 2":       "stepfun-2",
		"4router":         "4router",
		"hub linux do":    "hub-linux-do",
		"  spaced  ":      "spaced",
		"a/b\\c":          "a-b-c",
		"":                "",
		"...":             "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniqueProviderName(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["taken"] = config.Provider{Kind: config.KindClaude}
	used := map[string]bool{"chosen": true}

	cases := map[string]string{
		"free":    "free",
		"taken":   "taken2",
		"chosen":  "chosen2",
		"import":  "import2",    // reserved ak subcommand
		"Bili CC": "bili-cc",    // slugified
		"":        "claude-xyz", // fallback appType-id
	}
	for base, want := range cases {
		fallback := "claude-xyz"
		if got := uniqueProviderName(cfg, used, slugify(base), fallback); got != want {
			t.Errorf("uniqueProviderName(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestClaudeProviderFromCCSwitch(t *testing.T) {
	settings := `{"env":{
		"ANTHROPIC_AUTH_TOKEN":"sk-token",
		"ANTHROPIC_BASE_URL":"https://api.example",
		"ANTHROPIC_MODEL":"main",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":"h",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":"s",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":"o",
		"CLAUDE_CODE_SUBAGENT_MODEL":"sub",
		"NODE_EXTRA_CA_CERTS":"/etc/ssl/cert.pem"
	}}`
	p, notes := claudeProviderFromCCSwitch(settings)
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if p.Kind != config.KindClaude || p.BaseURL != "https://api.example" {
		t.Errorf("unexpected provider: %+v", p)
	}
	if p.APIKey != "sk-token" || p.KeyField != "auth_token" {
		t.Errorf("key mapping wrong: key=%q field=%q", p.APIKey, p.KeyField)
	}
	if p.Model != "main" || p.Haiku != "h" || p.Sonnet != "s" || p.Opus != "o" {
		t.Errorf("model mapping wrong: %+v", p)
	}
	// Extra keys must survive in Env, but the mapped ones must not be duplicated.
	if p.Env["CLAUDE_CODE_SUBAGENT_MODEL"] != "sub" || p.Env["NODE_EXTRA_CA_CERTS"] != "/etc/ssl/cert.pem" {
		t.Errorf("extra env lost: %v", p.Env)
	}
	if _, ok := p.Env["ANTHROPIC_BASE_URL"]; ok {
		t.Errorf("mapped key leaked into Env: %v", p.Env)
	}
}

func TestClaudeProviderFromCCSwitchAPIKey(t *testing.T) {
	p, _ := claudeProviderFromCCSwitch(`{"env":{"ANTHROPIC_API_KEY":"k","ANTHROPIC_BASE_URL":"https://x"}}`)
	if p.KeyField != "api_key" || p.APIKey != "k" {
		t.Errorf("api_key provider mapping wrong: %+v", p)
	}
}

func TestCodexProviderFromCCSwitch(t *testing.T) {
	settings := `{
		"auth":{"AICODING_API_KEY":"sk-live","OPENAI_API_KEY":"sk-openai"},
		"config":"model_provider = \"aicoding\"\nmodel = \"gpt-x\"\nmodel_reasoning_effort = \"high\"\n[model_providers.aicoding]\nname = \"aicoding\"\nbase_url = \"http://api/v1\"\nwire_api = \"responses\"\nenv_key = \"AICODING_API_KEY\"\n"
	}`
	p, notes := codexProviderFromCCSwitch(settings)
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if p.Kind != config.KindCodex || p.BaseURL != "http://api/v1" || p.Model != "gpt-x" {
		t.Errorf("unexpected provider: %+v", p)
	}
	if p.ProviderID != "aicoding" || p.WireAPI != "responses" || p.Reasoning != "high" {
		t.Errorf("unexpected codex fields: %+v", p)
	}
	// env_key wins over OPENAI_API_KEY.
	if p.APIKey != "sk-live" {
		t.Errorf("APIKey = %q, want sk-live", p.APIKey)
	}
}

func TestCodexKeyFromAuthFallback(t *testing.T) {
	if got := codexKeyFromAuth(map[string]string{"OPENAI_API_KEY": "k"}, ""); got != "k" {
		t.Errorf("fallback = %q, want k", got)
	}
	if got := codexKeyFromAuth(map[string]string{"B": "2", "A": "1"}, ""); got != "1" {
		t.Errorf("sorted fallback = %q, want 1", got)
	}
}

func TestCCSwitchCandidates(t *testing.T) {
	cfg := config.Default()
	raws := []ccswitch.Raw{
		{
			ID: "1", AppType: "claude", Name: "Bili Claude", IsCurrent: true,
			Settings: `{"env":{"ANTHROPIC_BASE_URL":"https://a","ANTHROPIC_AUTH_TOKEN":"k"}}`,
		},
		{
			ID: "2", AppType: "claude", Name: "Bili Claude", // duplicate name -> suffixed
			Settings: `{"env":{"ANTHROPIC_BASE_URL":"https://b"}}`,
		},
		{
			ID: "3", AppType: "claude", Name: "No URL",
			Settings: `{"env":{"ANTHROPIC_AUTH_TOKEN":"k"}}`,
		},
		{
			ID: "4", AppType: "codex", Name: "Step",
			Settings:  `{"auth":{"OPENAI_API_KEY":"k"},"config":"model_provider = \"s\"\n[model_providers.s]\nbase_url = \"\"\n"}`,
			Endpoints: []string{"https://ep1", "https://ep2"},
		},
		{ID: "5", AppType: "gemini", Name: "Gem", Settings: `{}`},
	}

	cands, unsupported := ccSwitchCandidates(cfg, raws)
	if unsupported["gemini"] != 1 {
		t.Errorf("unsupported = %v, want gemini:1", unsupported)
	}
	if len(cands) != 4 {
		t.Fatalf("got %d candidates, want 4", len(cands))
	}

	byName := map[string]ccCandidate{}
	for _, c := range cands {
		byName[c.name] = c
	}
	if _, ok := byName["bili-claude"]; !ok {
		t.Errorf("missing bili-claude: %v", byName)
	}
	if _, ok := byName["bili-claude2"]; !ok {
		t.Errorf("duplicate name not suffixed: %v", byName)
	}
	if c := byName["no-url"]; c.skip == "" {
		t.Errorf("provider without base_url should be skipped: %+v", c)
	}
	// Endpoint fallback fills the empty base URL and warns about the extra endpoint.
	step := byName["step"]
	if step.provider.BaseURL != "https://ep1" || step.skip != "" {
		t.Errorf("endpoint fallback failed: %+v", step)
	}
	if step.warning == "" {
		t.Errorf("expected a warning about multiple endpoints: %+v", step)
	}
	// A usable provider with only an informational warning is still pre-selected.
	if !step.ready {
		t.Errorf("provider with a key should be ready: %+v", step)
	}
	if byName["no-key"].ready {
		t.Errorf("provider without a key should not be ready: %+v", byName["no-key"])
	}
}
