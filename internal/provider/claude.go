// Package provider translates the configured providers into the environment
// variables and config files the shims need.
package provider

import (
	"sort"

	"github.com/abcdlsj/ak/internal/config"
)

// KV is an ordered environment variable key-value pair.
type KV struct {
	Key   string
	Value string
}

// EnvPlan is the full environment a shim injects: Set is exported, Unset is
// unset.
type EnvPlan struct {
	Set   []KV
	Unset []string
}

// claudeAuthKeys are the two mutually exclusive auth variables. Only one is
// written; the other must be unset.
const (
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"
	envAPIKey    = "ANTHROPIC_API_KEY"
)

// claudeProviderKeys is the complete set of provider-specific environment
// variables, taken from cc-switch's ENV_PROVIDER_SPECIFIC_EXCLUDES
// (src-tauri/src/services/provider/mod.rs:6414).
// The ones a shim does not set must be explicitly unset, otherwise they leak
// in from the parent process on a nested launch.
var claudeProviderKeys = []string{
	envAuthToken,
	envAPIKey,
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", // deprecated, always unset
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// ClaudeEnv computes the environment plan for a claude provider.
// An empty variant means no variant; key is the resolved plaintext secret.
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

	// Extra env vars merge last and can override the derived keys above.
	for k, v := range p.Env {
		env[k] = v
	}

	// A variant overrides the model. The built-in opus/sonnet/haiku tiers
	// replace the primary model directly.
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

// normalizeModels implements cc-switch's small_fast migration semantics
// (normalize_claude_models_in_value, mod.rs:7341):
//
//	haiku  <- current value -> small_fast -> model
//	sonnet <- current value -> model -> small_fast
//	opus   <- current value -> model -> small_fast
//
// An explicitly set tier is never rewritten, and ANTHROPIC_SMALL_FAST_MODEL is
// dropped in the end.
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

// unsetList returns the keys in known that are not in set, sorted.
// ANTHROPIC_SMALL_FAST_MODEL is deprecated and is force-unset even when it is
// configured.
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
		// small_fast is deprecated and never goes into the set list; it only
		// feeds normalizeModels as input.
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

// ClaudeVariants returns every variant name the provider's shim must recognise
// (built-in tiers plus custom ones), sorted.
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
