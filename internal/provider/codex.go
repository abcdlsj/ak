package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/pelletier/go-toml/v2"
)

// EnvKey derives the provider-specific secret environment variable name.
// Each provider gets its own variable, so a shim only ever exports its own and
// they cannot interfere with each other or with an existing
// OPENAI_API_KEY / AICODING_API_KEY.
func EnvKey(name string) string {
	var b strings.Builder
	b.WriteString("AK_KEY_")
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ProfileName is the codex profile name, mapping to
// ~/.codex/<ProfileName>.config.toml.
func ProfileName(name string) string { return "ak-" + name }

// CodexEnv computes the environment plan for a codex provider.
// The model and endpoint travel through the profile file; the environment only
// carries the secret.
func CodexEnv(name string, p config.Provider, key string) EnvPlan {
	env := map[string]string{}
	if key != "" {
		env[EnvKey(name)] = key
	}
	env["AK_PROVIDER"] = name
	if p.CodexHome != "" {
		env["CODEX_HOME"] = config.ExpandHome(p.CodexHome)
	}
	return EnvPlan{Set: sortedKV(env)}
}

// codexProviderID returns the table name for [model_providers.<id>].
func codexProviderID(name string, p config.Provider) string {
	if p.ProviderID != "" {
		return p.ProviderID
	}
	return name
}

// codexProfile is the structure of a generated profile file.
//
// It only writes scalar keys plus the single model_providers table. It never
// writes [[skills.config]], [projects."..."], [features] or [tui] — even if the
// layering replaced whole top-level tables, only the keys we intend to
// override would be affected, and the base config's skills and projects live
// under different top-level keys.
type codexProfile struct {
	ModelProvider  string                        `toml:"model_provider"`
	Model          string                        `toml:"model,omitempty"`
	ReasoningLevel string                        `toml:"model_reasoning_effort,omitempty"`
	ModelProviders map[string]codexProviderTable `toml:"model_providers"`
}

type codexProviderTable struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	WireAPI string `toml:"wire_api"`
	EnvKey  string `toml:"env_key"`
}

// CodexProfileTOML renders the profile file body, without the marker line,
// which the shim package adds uniformly.
func CodexProfileTOML(name string, p config.Provider) ([]byte, error) {
	id := codexProviderID(name, p)
	wire := p.WireAPI
	if wire == "" {
		wire = "responses"
	}
	prof := codexProfile{
		ModelProvider:  id,
		Model:          p.Model,
		ReasoningLevel: p.Reasoning,
		ModelProviders: map[string]codexProviderTable{
			id: {
				Name:    id,
				BaseURL: p.BaseURL,
				WireAPI: wire,
				EnvKey:  EnvKey(name),
			},
		},
	}
	data, err := toml.Marshal(prof)
	if err != nil {
		return nil, fmt.Errorf("render codex profile %q: %w", name, err)
	}
	return data, nil
}

// CodexVariants returns the variant names a codex shim recognises: the
// reasoning levels plus any custom variants.
func CodexVariants(p config.Provider) []string {
	seen := map[string]bool{}
	var out []string
	for v := range config.ValidReasoning() {
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
