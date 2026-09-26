package provider

import (
	"fmt"
	"path/filepath"
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

type codexEngine struct{}

func (codexEngine) Kind() config.Kind                      { return config.KindCodex }
func (codexEngine) BinName() string                        { return "codex" }
func (codexEngine) BinEnvVar() string                      { return "AK_CODEX_BIN" }
func (codexEngine) ConfiguredBin(s config.Settings) string { return s.CodexBin }

func (codexEngine) ArtifactDirs(ctx Context) []string {
	if ctx.CodexHome == "" {
		return nil
	}
	return []string{ctx.CodexHome}
}

// Launch layers a profile over the user's codex config; the model and endpoint
// travel through the profile, the environment only carries the secret.
func (codexEngine) Launch(name string, p config.Provider, key Secret, ctx Context) (Launch, error) {
	body, err := CodexProfileTOML(name, p)
	if err != nil {
		return Launch{}, err
	}
	l := Launch{
		Env:  CodexEnv(name, p, key),
		Args: []string{"--profile", ProfileName(name)},
	}
	if ctx.CodexHome != "" {
		l.Files = []File{{Path: filepath.Join(ctx.CodexHome, ProfileName(name)+".config.toml"), Body: body}}
	}
	for _, v := range mergeNames(config.ReasoningLevels(), p.Variants) {
		l.Variants = append(l.Variants, codexVariant(v, p))
	}
	return l, nil
}

// codexVariant turns a variant into -c overrides: a reasoning level sets the
// effort, a custom variant sets its model and effort.
func codexVariant(name string, p config.Provider) Variant {
	custom, ok := p.Variants[name]
	if !ok {
		return Variant{Name: name, Args: codexOverride("model_reasoning_effort", name)}
	}
	v := Variant{Name: name, Env: sortedMap(custom.Env), Shim: custom.Shim}
	if custom.Model != "" {
		v.Args = append(v.Args, codexOverride("model", custom.Model)...)
	}
	if custom.Reasoning != "" {
		v.Args = append(v.Args, codexOverride("model_reasoning_effort", custom.Reasoning)...)
	}
	return v
}

// codexOverride renders `-c key="value"`, the value as a TOML basic string.
func codexOverride(key, value string) []string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return []string{"-c", fmt.Sprintf(`%s="%s"`, key, r.Replace(value))}
}

// CodexEnv computes the environment plan for a codex provider.
func CodexEnv(name string, p config.Provider, key Secret) EnvPlan {
	env := newEnv()
	env.setSecret(EnvKey(name), key)
	env.set("AK_PROVIDER", name)
	if p.CodexHome != "" {
		env.set("CODEX_HOME", config.ExpandHome(p.CodexHome))
	}
	return EnvPlan{Set: env.sorted()}
}

// CodexProviderID returns the table name for [model_providers.<id>], which
// codex also records in its session logs.
func CodexProviderID(name string, p config.Provider) string {
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
	id := CodexProviderID(name, p)
	wire := p.WireAPI
	if wire == "" {
		wire = "responses"
	}
	prof := codexProfile{
		ModelProvider:  id,
		Model:          p.Model,
		ReasoningLevel: p.Reasoning,
		ModelProviders: map[string]codexProviderTable{
			id: {Name: id, BaseURL: p.BaseURL, WireAPI: wire, EnvKey: EnvKey(name)},
		},
	}
	data, err := toml.Marshal(prof)
	if err != nil {
		return nil, fmt.Errorf("render codex profile %q: %w", name, err)
	}
	return data, nil
}
