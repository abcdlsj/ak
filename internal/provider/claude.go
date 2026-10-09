// Package provider translates the configured providers into the environment
// variables, arguments and config files their commands need.
package provider

import (
	"encoding/json"
	"fmt"
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
func (claudeEngine) Launch(name string, p config.Provider, key Secret, ctx Context) (Launch, error) {
	// A pool talks to ak's own gateway; the members' keys stay on the gateway
	// side and never reach the command.
	if p.IsPool() && ctx.Gateway != "" {
		p = poolTarget(p, name, ctx.Gateway)
		key = Literal(poolKey)
	}
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
		// Built-in tiers are always recognised, so `ak-x opus` never reaches
		// claude as a prompt, but one that changes nothing or only passes the
		// alias through is not worth offering.
		quiet := !isCustom && (len(delta) == 0 || p.Model == "")
		variants = append(variants, Variant{Name: v, Env: delta, Shim: custom.Shim, Quiet: quiet})
	}
	args, err := ClaudeArgs(name, p)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Env: base, Args: args, Variants: variants, DefaultVariant: p.DefaultVariant}, nil
}

// ClaudeArgs passes the provider's own settings layer as --settings. Inline
// JSON rather than a file: nothing extra to write or reclaim, and the map is
// marshalled with sorted keys, so the command stays byte-stable.
func ClaudeArgs(name string, p config.Provider) ([]string, error) {
	if len(p.Settings) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(p.Settings)
	if err != nil {
		return nil, fmt.Errorf("provider %s settings: %w", name, err)
	}
	return []string{"--settings", string(b)}, nil
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
	// replace the primary model directly; with no model configured, the tier
	// alias itself is passed and claude resolves it.
	switch variant {
	case "opus":
		env.set("ANTHROPIC_MODEL", firstNonEmpty(opus, "opus"))
	case "sonnet":
		env.set("ANTHROPIC_MODEL", firstNonEmpty(sonnet, "sonnet"))
	case "haiku":
		env.set("ANTHROPIC_MODEL", firstNonEmpty(haiku, "haiku"))
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
	return mergeNames(config.ClaudeTiers(), p.Variants)
}

// mergeNames lists the built-in names in their given order, then the custom
// ones sorted.
func mergeNames[V any](builtin []string, custom map[string]V) []string {
	seen := map[string]bool{}
	out := append([]string(nil), builtin...)
	for _, v := range builtin {
		seen[v] = true
	}
	var extra []string
	for v := range custom {
		if !seen[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}
