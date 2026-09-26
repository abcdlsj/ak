// Package provider translates the configured providers into the environment
// variables, arguments and config files their commands need.
package provider

import (
	"sort"

	"github.com/abcdlsj/ak/internal/config"
)

// claudeAuthKeys are the two mutually exclusive auth variables. Only one is
// written; the other must be unset.
const (
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"
	envAPIKey    = "ANTHROPIC_API_KEY"
	envSmallFast = "ANTHROPIC_SMALL_FAST_MODEL"
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
	envSmallFast, // deprecated, always unset
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

type claudeEngine struct{}

func (claudeEngine) Kind() config.Kind                      { return config.KindClaude }
func (claudeEngine) BinName() string                        { return "claude" }
func (claudeEngine) BinEnvVar() string                      { return "AK_CLAUDE_BIN" }
func (claudeEngine) ConfiguredBin(s config.Settings) string { return s.ClaudeBin }
func (claudeEngine) ArtifactDirs(Context) []string          { return nil }

// Launch sets the environment; each variant carries only the variables that
// differ from the base.
func (claudeEngine) Launch(name string, p config.Provider, key Secret, _ Context) (Launch, error) {
	base := ClaudeEnv(name, p, "", key)
	baseVals := map[string]string{}
	for _, kv := range base.Set {
		baseVals[kv.Key] = kv.Value
	}

	var variants []Variant
	for _, v := range claudeVariantNames(p) {
		var delta []KV
		for _, kv := range ClaudeEnv(name, p, v, key).Set {
			if baseVals[kv.Key] != kv.Value {
				delta = append(delta, kv)
			}
		}
		custom, isCustom := p.Variants[v]
		// A built-in tier that changes nothing need not be recognised.
		if len(delta) == 0 && !isCustom {
			continue
		}
		variants = append(variants, Variant{Name: v, Env: delta, Shim: custom.Shim})
	}
	return Launch{Env: base, Variants: variants}, nil
}

// ClaudeEnv computes the environment plan for a claude provider.
// An empty variant means no variant.
func ClaudeEnv(name string, p config.Provider, variant string, key Secret) EnvPlan {
	env := newEnv()
	env.set("ANTHROPIC_BASE_URL", p.BaseURL)

	authKey := envAuthToken
	if p.KeyField == "api_key" {
		authKey = envAPIKey
	}
	env.setSecret(authKey, key)

	model, haiku, sonnet, opus := normalizeModels(p)
	env.setIf("ANTHROPIC_MODEL", model)
	env.setIf("ANTHROPIC_DEFAULT_HAIKU_MODEL", haiku)
	env.setIf("ANTHROPIC_DEFAULT_SONNET_MODEL", sonnet)
	env.setIf("ANTHROPIC_DEFAULT_OPUS_MODEL", opus)

	// Extra env vars merge last and can override the derived keys above.
	for k, v := range p.Env {
		env.set(k, v)
	}

	// A variant overrides the model. The built-in opus/sonnet/haiku tiers
	// replace the primary model directly.
	switch variant {
	case "opus":
		env.setIf("ANTHROPIC_MODEL", opus)
	case "sonnet":
		env.setIf("ANTHROPIC_MODEL", sonnet)
	case "haiku":
		env.setIf("ANTHROPIC_MODEL", haiku)
	}
	if v, ok := p.Variants[variant]; ok && variant != "" {
		env.setIf("ANTHROPIC_MODEL", v.Model)
		for k, val := range v.Env {
			env.set(k, val)
		}
	}

	env.set("AK_PROVIDER", name)
	if p.ConfigDir != "" {
		env.set("CLAUDE_CONFIG_DIR", config.ExpandHome(p.ConfigDir))
	}

	// small_fast is deprecated: it only feeds normalizeModels and is always unset.
	return EnvPlan{Set: env.sorted(envSmallFast), Unset: unsetList(claudeProviderKeys, env)}
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
	smallFast := p.Env[envSmallFast]

	haiku = firstNonEmpty(p.Haiku, smallFast, model)
	sonnet = firstNonEmpty(p.Sonnet, model, smallFast)
	opus = firstNonEmpty(p.Opus, model, smallFast)
	return
}

// unsetList returns the keys in known that are not set, sorted.
// ANTHROPIC_SMALL_FAST_MODEL is deprecated and is force-unset even when it is
// configured.
func unsetList(known []string, env *envBuilder) []string {
	var out []string
	for _, k := range known {
		if k == envSmallFast || !env.has(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// claudeVariantNames returns every variant name the provider's command may
// recognise (built-in tiers plus custom ones), sorted.
func claudeVariantNames(p config.Provider) []string {
	return mergeNames(config.ImplicitClaudeVariants(), p.Variants)
}

func mergeNames[V any](builtin map[string]bool, custom map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for v := range builtin {
		seen[v] = true
		out = append(out, v)
	}
	for v := range custom {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
