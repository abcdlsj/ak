package provider

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
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

// Launch passes the provider, its endpoint and its model to codex as -c
// overrides; the environment only carries the secret. Overrides are used
// instead of a <name>.config.toml profile because codex persists the config it
// changes (its TUI notices, for one) into the active profile file, and that
// write can drop the provider we put there, silently sending the next launch to
// the base config's default provider.
func (codexEngine) Launch(name string, p config.Provider, key Secret, ctx Context) (Launch, error) {
	if p.IsPool() && ctx.Gateway != "" {
		p = poolTarget(p, name, ctx.Gateway)
		key = Literal(poolKey)
	}
	l := Launch{
		Env:  CodexEnv(name, p, key),
		Args: CodexArgs(name, p),
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

// CodexArgs renders the -c overrides that select the provider, its endpoint and
// its model. The model_providers table is passed whole with the provider's id
// as a quoted key, so an id containing a dot still parses (a dotted path would
// split it).
func CodexArgs(name string, p config.Provider) []string {
	id := CodexProviderID(name, p)
	wire := p.WireAPI
	if wire == "" {
		wire = "responses"
	}
	table := fmt.Sprintf("{%s={name=%s,base_url=%s,wire_api=%s,env_key=%s}}",
		tomlString(id), tomlString(id), tomlString(p.BaseURL), tomlString(wire), tomlString(EnvKey(name)))
	args := codexRaw("model_provider", tomlString(id))
	if p.Model != "" {
		args = append(args, codexRaw("model", tomlString(p.Model))...)
	}
	if p.Reasoning != "" {
		args = append(args, codexRaw("model_reasoning_effort", tomlString(p.Reasoning))...)
	}
	return append(args, codexRaw("model_providers", table)...)
}

// codexOverride renders `-c key="value"`, the value as a TOML basic string.
func codexOverride(key, value string) []string {
	return codexRaw(key, tomlString(value))
}

// codexRaw renders `-c key=<raw>`, the value used exactly as written: a TOML
// table is not a string, so it cannot go through tomlString.
func codexRaw(key, raw string) []string {
	return []string{"-c", key + "=" + raw}
}

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// CodexEnv computes the environment plan for a codex provider.
func CodexEnv(name string, p config.Provider, key Secret) EnvPlan {
	env := newEnv()
	env.setSecret(EnvKey(name), key)
	env.set("AK_PROVIDER", name)
	if p.CodexHome != "" {
		env.set("CODEX_HOME", config.ExpandHome(p.CodexHome))
	}
	// Extra env vars merge last, as for claude.
	for k, v := range p.Env {
		env.set(k, v)
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
