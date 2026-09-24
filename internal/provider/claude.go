// Package provider 把配置里的供应商翻译成 shim 需要的环境变量与配置文件。
package provider

import (
	"sort"

	"github.com/abcdlsj/ak/internal/config"
)

// KV 是一个有序的环境变量键值对。
type KV struct {
	Key   string
	Value string
}

// EnvPlan 是 shim 要注入的完整环境:Set 逐条 export,Unset 逐条 unset。
type EnvPlan struct {
	Set   []KV
	Unset []string
}

// claudeAuthKeys 是两个互斥的鉴权变量。只写一个,另一个必须 unset。
const (
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"
	envAPIKey    = "ANTHROPIC_API_KEY"
)

// claudeProviderKeys 是供应商专属的环境变量全集,来自 cc-switch 的
// ENV_PROVIDER_SPECIFIC_EXCLUDES(src-tauri/src/services/provider/mod.rs:6414)。
// shim 不设置的那些必须显式 unset,否则嵌套启动时会从父进程泄漏进来。
var claudeProviderKeys = []string{
	envAuthToken,
	envAPIKey,
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", // 已废弃,一律 unset
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// ClaudeEnv 计算 claude 供应商的环境变量方案。
// variant 为空表示不使用变体;key 是已解析出的明文密钥。
func ClaudeEnv(name string, p config.Provider, variant string, key string) EnvPlan {
	env := map[string]string{}

	env["ANTHROPIC_BASE_URL"] = p.BaseURL

	authKey := envAuthToken
	if p.KeyField == "api_key" {
		authKey = envAPIKey
	}
	if key != "" {
		env[authKey] = key
	}

	model, haiku, sonnet, opus := normalizeModels(p)
	setIf(env, "ANTHROPIC_MODEL", model)
	setIf(env, "ANTHROPIC_DEFAULT_HAIKU_MODEL", haiku)
	setIf(env, "ANTHROPIC_DEFAULT_SONNET_MODEL", sonnet)
	setIf(env, "ANTHROPIC_DEFAULT_OPUS_MODEL", opus)

	// 长尾 env 最后合并,可覆盖上面的派生键。
	for k, v := range p.Env {
		env[k] = v
	}

	// 变体覆盖模型。内置档位 opus/sonnet/haiku 直接改主模型。
	if variant != "" {
		switch variant {
		case "opus":
			setIf(env, "ANTHROPIC_MODEL", opus)
		case "sonnet":
			setIf(env, "ANTHROPIC_MODEL", sonnet)
		case "haiku":
			setIf(env, "ANTHROPIC_MODEL", haiku)
		}
		if v, ok := p.Variants[variant]; ok {
			setIf(env, "ANTHROPIC_MODEL", v.Model)
			for k, val := range v.Env {
				env[k] = val
			}
		}
	}

	env["AK_PROVIDER"] = name
	if p.ConfigDir != "" {
		env["CLAUDE_CONFIG_DIR"] = config.ExpandHome(p.ConfigDir)
	}

	return EnvPlan{Set: sortedKV(env), Unset: unsetList(claudeProviderKeys, env)}
}

// normalizeModels 实现 cc-switch 的 small_fast 迁移语义
// (normalize_claude_models_in_value, mod.rs:7341):
// haiku  ← 现值 → small_fast → model
// sonnet ← 现值 → model → small_fast
// opus   ← 现值 → model → small_fast
// 已显式指定的档位不被改写,ANTHROPIC_SMALL_FAST_MODEL 最终丢弃。
func normalizeModels(p config.Provider) (model, haiku, sonnet, opus string) {
	model = p.Model
	smallFast := p.Env["ANTHROPIC_SMALL_FAST_MODEL"]

	haiku = firstNonEmpty(p.Haiku, smallFast, model)
	sonnet = firstNonEmpty(p.Sonnet, model, smallFast)
	opus = firstNonEmpty(p.Opus, model, smallFast)
	return
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func setIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// unsetList 返回 known 中未被 set 的键,排序后输出。
// ANTHROPIC_SMALL_FAST_MODEL 已废弃,即使配置里写了也强制 unset。
func unsetList(known []string, set map[string]string) []string {
	var out []string
	for _, k := range known {
		if k == "ANTHROPIC_SMALL_FAST_MODEL" {
			out = append(out, k)
			continue
		}
		if _, ok := set[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKV(m map[string]string) []KV {
	keys := make([]string, 0, len(m))
	for k := range m {
		// small_fast 已废弃,不进 set 列表(它只在 normalizeModels 里当输入)。
		if k == "ANTHROPIC_SMALL_FAST_MODEL" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KV, 0, len(keys))
	for _, k := range keys {
		out = append(out, KV{Key: k, Value: m[k]})
	}
	return out
}

// ClaudeVariants 返回该供应商 shim 需要识别的全部变体名(内置档位 + 自定义),已排序。
func ClaudeVariants(p config.Provider) []string {
	seen := map[string]bool{}
	var out []string
	for v := range config.ImplicitClaudeVariants() {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for v := range p.Variants {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
