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
