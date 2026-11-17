package provider

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func envMap(p EnvPlan) map[string]string {
	m := map[string]string{}
	for _, kv := range p.Set {
		m[kv.Key] = kv.Value
	}
	return m
}

func unsetSet(p EnvPlan) map[string]bool {
	m := map[string]bool{}
	for _, k := range p.Unset {
		m[k] = true
	}
	return m
}

// TestNormalizeModels follows the semantics of cc-switch's
// normalize_claude_models_in_value (src-tauri/src/services/provider/mod.rs:7341):
// haiku <- current value -> small_fast -> model;
// sonnet/opus <- current value -> model -> small_fast.
func TestNormalizeModels(t *testing.T) {
	tests := []struct {
		name                            string
		p                               config.Provider
		wantHaiku, wantSonnet, wantOpus string
	}{
		{
			name:       "model only: all three tiers inherit model",
			p:          config.Provider{Model: "m1"},
			wantHaiku:  "m1",
			wantSonnet: "m1",
			wantOpus:   "m1",
		},
		{
			name:       "small_fast only: haiku uses it, sonnet/opus fall back to it",
			p:          config.Provider{Env: map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"}},
			wantHaiku:  "sf",
			wantSonnet: "sf",
			wantOpus:   "sf",
		},
		{
			name: "both present: haiku prefers small_fast, sonnet/opus prefer model",
			p: config.Provider{
				Model: "m1",
				Env:   map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"},
			},
			wantHaiku:  "sf",
			wantSonnet: "m1",
			wantOpus:   "m1",
		},
		{
			name: "all three tiers explicit: must not be rewritten",
			p: config.Provider{
				Model: "m1", Haiku: "h", Sonnet: "s", Opus: "o",
				Env: map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"},
			},
			wantHaiku:  "h",
			wantSonnet: "s",
			wantOpus:   "o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, h, s, o := normalizeModels(tt.p)
			if h != tt.wantHaiku {
				t.Errorf("haiku = %q, want %q", h, tt.wantHaiku)
			}
			if s != tt.wantSonnet {
				t.Errorf("sonnet = %q, want %q", s, tt.wantSonnet)
			}
			if o != tt.wantOpus {
				t.Errorf("opus = %q, want %q", o, tt.wantOpus)
			}
		})
	}
}

// TestClaudeEnv_SmallFastAlwaysUnset checks that the deprecated variable is
// never exported and is always unset.
func TestClaudeEnv_SmallFastAlwaysUnset(t *testing.T) {
	p := config.Provider{
		Kind: config.KindClaude, BaseURL: "https://x", Model: "m",
		Env: map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"},
	}
	plan := ClaudeEnv("x", p, "", Literal("k"))

	if v, ok := envMap(plan)["ANTHROPIC_SMALL_FAST_MODEL"]; ok {
		t.Errorf("deprecated variable was exported: %q", v)
	}
	if !unsetSet(plan)["ANTHROPIC_SMALL_FAST_MODEL"] {
		t.Error("deprecated variable must appear in the unset list")
	}
}

// TestClaudeEnv_KeyFieldExclusive checks that only one of the two auth
// variables is written and the other is unset. Otherwise it would leak in from
// the parent process on a nested launch and silently route to the wrong
// provider.
func TestClaudeEnv_KeyFieldExclusive(t *testing.T) {
	tests := []struct {
		keyField  string
		wantSet   string
		wantUnset string
	}{
		{"", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"},
		{"auth_token", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"},
		{"api_key", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
	}
	for _, tt := range tests {
		t.Run("key_field="+tt.keyField, func(t *testing.T) {
			p := config.Provider{
				Kind: config.KindClaude, BaseURL: "https://x",
				Model: "m", KeyField: tt.keyField,
			}
			plan := ClaudeEnv("x", p, "", Literal("secret"))
			env := envMap(plan)

			if env[tt.wantSet] != "secret" {
				t.Errorf("%s should be secret, got %q", tt.wantSet, env[tt.wantSet])
			}
			if _, ok := env[tt.wantUnset]; ok {
				t.Errorf("%s should not be exported", tt.wantUnset)
			}
			if !unsetSet(plan)[tt.wantUnset] {
				t.Errorf("%s must appear in the unset list", tt.wantUnset)
			}
		})
	}
}

// TestClaudeEnv_TailEnvOverrides checks that [providers.x.env] can override
// derived keys.
func TestClaudeEnv_TailEnvOverrides(t *testing.T) {
	p := config.Provider{
		Kind: config.KindClaude, BaseURL: "https://x", Model: "m",
		Env: map[string]string{
			"ANTHROPIC_MODEL": "override",
			"API_TIMEOUT_MS":  "600000",
		},
	}
	env := envMap(ClaudeEnv("x", p, "", Literal("k")))

	if env["ANTHROPIC_MODEL"] != "override" {
		t.Errorf("ANTHROPIC_MODEL = %q, want the extra env to override it with \"override\"", env["ANTHROPIC_MODEL"])
	}
	if env["API_TIMEOUT_MS"] != "600000" {
		t.Errorf("extra env not applied: %q", env["API_TIMEOUT_MS"])
	}
}

// TestClaudeEnv_NoGlobalPreferences checks that global preference keys are not
// injected by the shim. They keep working from settings.json and the shim must
// leave them alone.
func TestClaudeEnv_NoGlobalPreferences(t *testing.T) {
	p := config.Provider{Kind: config.KindClaude, BaseURL: "https://x", Model: "m"}
	plan := ClaudeEnv("x", p, "", Literal("k"))
	env := envMap(plan)
	unset := unsetSet(plan)

	for _, k := range []string{
		"NODE_EXTRA_CA_CERTS",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
	} {
		if _, ok := env[k]; ok {
			t.Errorf("global preference %s must not be exported by the shim", k)
		}
		if unset[k] {
			t.Errorf("global preference %s must not be unset by the shim", k)
		}
	}
}

// TestClaudeEnv_Deterministic checks that the output order is stable, which is
// what makes the shim idempotent.
func TestClaudeEnv_Deterministic(t *testing.T) {
	p := config.Provider{
		Kind: config.KindClaude, BaseURL: "https://x", Model: "m",
		Env: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5"},
	}
	first := ClaudeEnv("x", p, "", Literal("k"))
	for i := 0; i < 20; i++ {
		got := ClaudeEnv("x", p, "", Literal("k"))
		if len(got.Set) != len(first.Set) {
			t.Fatal("length is unstable")
		}
		for j := range got.Set {
			if got.Set[j] != first.Set[j] {
				t.Fatalf("iteration %d differs in order: %v vs %v", i, got.Set[j], first.Set[j])
			}
		}
	}
}

func TestEnvKey(t *testing.T) {
	tests := map[string]string{
		"cpa":       "AK_KEY_CPA",
		"bili-luna": "AK_KEY_BILI_LUNA",
		"my.site":   "AK_KEY_MY_SITE",
		"a1":        "AK_KEY_A1",
	}
	for in, want := range tests {
		if got := EnvKey(in); got != want {
			t.Errorf("EnvKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCodexArgsSelectProvider is a machine-checked guard: the provider, its
// endpoint and the key's variable name must reach codex, and the key itself
// must not.
func TestCodexArgsSelectProvider(t *testing.T) {
	p := config.Provider{
		Kind: config.KindCodex, BaseURL: "https://cpa.example/v1",
		Model: "gpt-x", Reasoning: "max", WireAPI: "responses",
	}
	joined := ""
	for _, a := range CodexArgs("cpa", p) {
		joined += a + " "
	}
	for _, want := range []string{
		`model_provider="cpa"`,
		`model="gpt-x"`,
		`model_reasoning_effort="max"`,
		`base_url="https://cpa.example/v1"`,
		`wire_api="responses"`,
		`env_key="AK_KEY_CPA"`,
	} {
		if !contains(joined, want) {
			t.Errorf("codex args are missing %q:\n%s", want, joined)
		}
	}
}

// TestCodexArgsDottedProviderID guards the inline model_providers table: a
// dotted id would split a dotted path, so the id goes in as a quoted TOML key.
func TestCodexArgsDottedProviderID(t *testing.T) {
	p := config.Provider{Kind: config.KindCodex, BaseURL: "https://x/v1", Model: "m"}
	joined := ""
	for _, a := range CodexArgs("my.site", p) {
		joined += a + " "
	}
	if !contains(joined, `model_provider="my.site"`) {
		t.Errorf("dotted provider id lost:\n%s", joined)
	}
	if !contains(joined, `{"my.site"=`) {
		t.Errorf("dotted provider key is not quoted:\n%s", joined)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestCodexArgsModelCatalog guards the optional model_catalog override: a
// provider that sets it passes the path through as model_catalog_json, and one
// that does not leaves codex's built-in catalog alone.
func TestCodexArgsModelCatalog(t *testing.T) {
	p := config.Provider{
		Kind: config.KindCodex, BaseURL: "https://x/v1", Model: "m",
		ModelCatalog: "~/.codex/model-catalogs/deepseek.json",
	}
	joined := ""
	for _, a := range CodexArgs("dsccodex", p) {
		joined += a + " "
	}
	for _, want := range []string{`model_catalog_json="`, `model-catalogs/deepseek.json"`} {
		if !contains(joined, want) {
			t.Errorf("model catalog override is missing %q:\n%s", want, joined)
		}
	}
	plain := config.Provider{Kind: config.KindCodex, BaseURL: "https://x/v1", Model: "m"}
	for _, a := range CodexArgs("plain", plain) {
		if contains(a, "model_catalog_json") {
			t.Errorf("a provider without model_catalog must not set it: %q", a)
		}
	}
}

// TestClaudeEnvNumbers guards the two numbers claude cannot know: a model it has
// no metadata for gets a 1M window and 32000 tokens of reply otherwise. One
// value serves every such model, the tiers included, so the smallest of the
// provider's wins.
func TestClaudeEnvNumbers(t *testing.T) {
	// The cache lives where the test puts it, so the table is the test's.
	t.Setenv(config.DirEnv, t.TempDir())

	p := config.Provider{BaseURL: "https://x", Model: "m", ContextWindow: 400000, MaxTokens: 64000}
	env := envMap(ClaudeEnv("x", p, "", Literal("k")))
	if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "400000" || env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "64000" {
		t.Errorf("numbers = %q/%q", env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"])
	}

	// Nothing known: left out, so claude keeps its own defaults.
	env = envMap(ClaudeEnv("x", config.Provider{BaseURL: "https://x", Model: "m"}, "", Literal("k")))
	if _, ok := env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; ok {
		t.Error("a window was passed for a model ak knows nothing about")
	}
	if _, ok := env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"]; ok {
		t.Error("a reply limit was passed for a model ak knows nothing about")
	}

	// An extra env var still wins, as for every other derived key.
	p = config.Provider{
		BaseURL: "https://x", Model: "m", ContextWindow: 400000,
		Env: map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "1000000"},
	}
	if got := envMap(ClaudeEnv("x", p, "", Literal("k")))["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; got != "1000000" {
		t.Errorf("env did not override the derived window: %q", got)
	}
}

// TestClaudeNumbers checks the two values claude gets: the window is the
// smallest of the provider's models, since one value serves every model it does
// not know, while the reply limit is the primary model's alone — claude applies
// it to the models it does know too.
func TestClaudeNumbers(t *testing.T) {
	writeCatalog(t, `{"big":{"context":1000000,"max_output":128000},"small":{"context":131072,"max_output":32000}}`)

	p := config.Provider{Model: "big", Haiku: "small"}
	if window, output := claudeNumbers(p); window != 131072 || output != 128000 {
		t.Errorf("numbers = %d/%d, want the tier's window and the primary's reply", window, output)
	}

	// A variant's model is one of the provider's too.
	p = config.Provider{Model: "big", Variants: map[string]config.Variant{"cheap": {Model: "small"}}}
	if window, _ := claudeNumbers(p); window != 131072 {
		t.Errorf("window = %d, want the variant's 131072", window)
	}

	// A tier left out of the provider falls back to the primary model, which is
	// then the only one that answers.
	p = config.Provider{Model: "big"}
	if window, output := claudeNumbers(p); window != 1000000 || output != 128000 {
		t.Errorf("numbers = %d/%d, want the primary's", window, output)
	}

	// A provider that states its own numbers gets them even with no model named.
	p = config.Provider{ContextWindow: 400000, MaxTokens: 64000}
	if window, output := claudeNumbers(p); window != 400000 || output != 64000 {
		t.Errorf("numbers = %d/%d, want the provider's own", window, output)
	}
}

// TestCodexArgsContextWindow guards the window ak hands codex: without it codex
// takes a model it does not know for a 272K one, which is too much for a small
// endpoint and too little for a large one.
func TestCodexArgsContextWindow(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	args := func(p config.Provider) string {
		joined := ""
		for _, a := range CodexArgs("x", p) {
			joined += a + " "
		}
		return joined
	}

	p := config.Provider{Kind: config.KindCodex, BaseURL: "https://x/v1", Model: "m", ContextWindow: 131072}
	if got := args(p); !contains(got, "model_context_window=131072") {
		t.Errorf("the window is missing:\n%s", got)
	}

	// A provider that brings its own catalog says what the window is; codex
	// clamps this to the catalog's max_context_window anyway.
	p.ModelCatalog = "~/.codex/catalogs/deepseek.json"
	if got := args(p); contains(got, "model_context_window") {
		t.Errorf("the window was passed beside a model_catalog:\n%s", got)
	}
	p.ModelCatalog = ""

	// Nothing known: left out, so codex keeps its own numbers.
	if got := args(config.Provider{Kind: config.KindCodex, BaseURL: "https://x/v1", Model: "m"}); contains(got, "model_context_window") {
		t.Errorf("a window was passed for a model ak knows nothing about:\n%s", got)
	}

	// The catalog answers when the provider does not.
	writeCatalog(t, `{"m":{"context":200000}}`)
	if got := args(config.Provider{Kind: config.KindCodex, BaseURL: "https://x/v1", Model: "m"}); !contains(got, "model_context_window=200000") {
		t.Errorf("the table's window was not used:\n%s", got)
	}
}

func TestClaudeEnv_NativeOAuth(t *testing.T) {
	p := config.Provider{Kind: config.KindClaude, ConfigDir: "/tmp/claude-pro"}
	plan := ClaudeEnv("pro", p, "", Literal(""))
	env := envMap(plan)
	unset := unsetSet(plan)

	if env["CLAUDE_CONFIG_DIR"] != "/tmp/claude-pro" || env[EnvAgentHome] != "/tmp/claude-pro" {
		t.Fatalf("home env = %v", env)
	}
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s must not be set for native claude", k)
		}
		if !unset[k] {
			t.Errorf("%s must be unset for native claude", k)
		}
	}
}

func TestCodexNativeOAuth(t *testing.T) {
	p := config.Provider{Kind: config.KindCodex, CodexHome: "/tmp/codex-pro"}
	l, err := codexEngine{}.Launch("cxpro", p, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, a := range l.Args {
		joined += a + " "
	}
	if !contains(joined, `cli_auth_credentials_store="file"`) {
		t.Errorf("missing file credential store override: %s", joined)
	}
	if contains(joined, "model_providers") || contains(joined, "model_provider=") {
		t.Errorf("native codex must not override model_provider: %s", joined)
	}
	env := envMap(l.Env)
	if env["CODEX_HOME"] != "/tmp/codex-pro" || env[EnvAgentHome] != "/tmp/codex-pro" {
		t.Fatalf("home env = %v", env)
	}
	if _, ok := env[EnvKey("cxpro")]; ok {
		t.Errorf("native codex must not export %s", EnvKey("cxpro"))
	}
	if !unsetSet(l.Env)["OPENAI_API_KEY"] {
		t.Error("native codex must unset OPENAI_API_KEY unless provider.env sets it")
	}
}
