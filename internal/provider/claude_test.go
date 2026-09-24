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

// TestNormalizeModels 照 cc-switch 的 normalize_claude_models_in_value 语义
// (src-tauri/src/services/provider/mod.rs:7341):
// haiku ← 现值 → small_fast → model;sonnet/opus ← 现值 → model → small_fast。
func TestNormalizeModels(t *testing.T) {
	tests := []struct {
		name                            string
		p                               config.Provider
		wantHaiku, wantSonnet, wantOpus string
	}{
		{
			name:       "只有 model:三档全部继承 model",
			p:          config.Provider{Model: "m1"},
			wantHaiku:  "m1",
			wantSonnet: "m1",
			wantOpus:   "m1",
		},
		{
			name:       "只有 small_fast:haiku 用它,sonnet/opus 回落到它",
			p:          config.Provider{Env: map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"}},
			wantHaiku:  "sf",
			wantSonnet: "sf",
			wantOpus:   "sf",
		},
		{
			name: "两者都有:haiku 优先 small_fast,sonnet/opus 优先 model",
			p: config.Provider{
				Model: "m1",
				Env:   map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"},
			},
			wantHaiku:  "sf",
			wantSonnet: "m1",
			wantOpus:   "m1",
		},
		{
			name: "三档已显式指定:不得被改写",
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
				t.Errorf("haiku = %q, 期望 %q", h, tt.wantHaiku)
			}
			if s != tt.wantSonnet {
				t.Errorf("sonnet = %q, 期望 %q", s, tt.wantSonnet)
			}
			if o != tt.wantOpus {
				t.Errorf("opus = %q, 期望 %q", o, tt.wantOpus)
			}
		})
	}
}

// TestClaudeEnv_SmallFastAlwaysUnset 确认已废弃的变量永不被 export 且一定 unset。
func TestClaudeEnv_SmallFastAlwaysUnset(t *testing.T) {
	p := config.Provider{
		Kind: config.KindClaude, BaseURL: "https://x", Model: "m",
		Env: map[string]string{"ANTHROPIC_SMALL_FAST_MODEL": "sf"},
	}
	plan := ClaudeEnv("x", p, "", "k")

	if v, ok := envMap(plan)["ANTHROPIC_SMALL_FAST_MODEL"]; ok {
		t.Errorf("已废弃变量被 export 了: %q", v)
	}
	if !unsetSet(plan)["ANTHROPIC_SMALL_FAST_MODEL"] {
		t.Error("已废弃变量必须出现在 unset 列表里")
	}
}

// TestClaudeEnv_KeyFieldExclusive 确认两个鉴权变量只写一个,另一个必须 unset。
// 否则嵌套启动时会从父进程泄漏,导致静默走错供应商。
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
			plan := ClaudeEnv("x", p, "", "secret")
			env := envMap(plan)

			if env[tt.wantSet] != "secret" {
				t.Errorf("%s 应为 secret,实际 %q", tt.wantSet, env[tt.wantSet])
			}
			if _, ok := env[tt.wantUnset]; ok {
				t.Errorf("%s 不该被 export", tt.wantUnset)
			}
			if !unsetSet(plan)[tt.wantUnset] {
				t.Errorf("%s 必须出现在 unset 列表里", tt.wantUnset)
			}
		})
	}
}

// TestClaudeEnv_TailEnvOverrides 确认 [providers.x.env] 能覆盖派生键。
func TestClaudeEnv_TailEnvOverrides(t *testing.T) {
	p := config.Provider{
		Kind: config.KindClaude, BaseURL: "https://x", Model: "m",
		Env: map[string]string{
			"ANTHROPIC_MODEL": "override",
			"API_TIMEOUT_MS":  "600000",
		},
	}
	env := envMap(ClaudeEnv("x", p, "", "k"))

	if env["ANTHROPIC_MODEL"] != "override" {
		t.Errorf("ANTHROPIC_MODEL = %q, 期望被长尾 env 覆盖为 override", env["ANTHROPIC_MODEL"])
	}
	if env["API_TIMEOUT_MS"] != "600000" {
		t.Errorf("长尾 env 未生效: %q", env["API_TIMEOUT_MS"])
	}
}

// TestClaudeEnv_NoGlobalPreferences 确认全局偏好键不会被 shim 注入。
// 它们留在 settings.json 里继续生效,shim 不该碰。
func TestClaudeEnv_NoGlobalPreferences(t *testing.T) {
	p := config.Provider{Kind: config.KindClaude, BaseURL: "https://x", Model: "m"}
	plan := ClaudeEnv("x", p, "", "k")
	env := envMap(plan)
	unset := unsetSet(plan)

	for _, k := range []string{
		"NODE_EXTRA_CA_CERTS",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
	} {
		if _, ok := env[k]; ok {
			t.Errorf("全局偏好 %s 不该被 shim export", k)
		}
		if unset[k] {
			t.Errorf("全局偏好 %s 不该被 shim unset", k)
		}
	}
}

// TestClaudeEnv_Deterministic 确认输出顺序确定,这是 shim 幂等的前提。
func TestClaudeEnv_Deterministic(t *testing.T) {
	p := config.Provider{
		Kind: config.KindClaude, BaseURL: "https://x", Model: "m",
		Env: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5"},
	}
	first := ClaudeEnv("x", p, "", "k")
	for i := 0; i < 20; i++ {
		got := ClaudeEnv("x", p, "", "k")
		if len(got.Set) != len(first.Set) {
			t.Fatal("长度不稳定")
		}
		for j := range got.Set {
			if got.Set[j] != first.Set[j] {
				t.Fatalf("第 %d 次迭代顺序不同: %v vs %v", i, got.Set[j], first.Set[j])
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
			t.Errorf("EnvKey(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestCodexProfileTOML_NoArrayTables 是机器化守卫:
// profile 文件绝不能含数组表,否则万一层叠语义是整表替换,
// base config 里的 [[skills.config]] 与 [projects.*] 会丢。
func TestCodexProfileTOML_NoArrayTables(t *testing.T) {
	p := config.Provider{
		Kind: config.KindCodex, BaseURL: "https://cpa.example/v1",
		Model: "gpt-x", Reasoning: "max", WireAPI: "responses",
	}
	data, err := CodexProfileTOML("cpa", p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if contains(s, "[[") {
		t.Errorf("profile 含数组表,会威胁 base config:\n%s", s)
	}
	for _, forbidden := range []string{"skills", "projects", "features", "tui", "hooks"} {
		if contains(s, forbidden) {
			t.Errorf("profile 含不该出现的键 %q:\n%s", forbidden, s)
		}
	}
	// 必要键要在。
	for _, want := range []string{"model_provider", "base_url", "env_key", "AK_KEY_CPA"} {
		if !contains(s, want) {
			t.Errorf("profile 缺少 %q:\n%s", want, s)
		}
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
