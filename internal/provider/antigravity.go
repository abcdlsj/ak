package provider

import (
	"github.com/abcdlsj/ak/internal/config"
)

// EnvSSHConnection is what makes agy keep its OAuth credential in a file
// instead of the OS keyring. agy treats any of SSH_CONNECTION, SSH_TTY and
// SSH_CLIENT as "this is a remote session": the browser cannot call back, so a
// keyring is not reliable, and it falls back to
// <gemini_dir>/<app_data_dir>/antigravity-oauth-token. ak relies on that: the
// file lives inside the provider's own gemini_dir, which is what makes one
// provider one account.
const EnvSSHConnection = "SSH_CONNECTION"

// AntigravitySSHConnection is the value exported in EnvSSHConnection. agy only
// checks that the variable is set, but a loopback value keeps anything that
// parses it (a nested ssh, a status line) sane.
const AntigravitySSHConnection = "127.0.0.1 50000 127.0.0.1 22"

type agyEngine struct{}

func (agyEngine) Kind() config.Kind                      { return config.KindAntigravity }
func (agyEngine) BinName() string                        { return "agy" }
func (agyEngine) BinEnvVar() string                      { return "AK_AGY_BIN" }
func (agyEngine) ConfiguredBin(s config.Settings) string { return s.AntigravityBin }

// ArtifactDirs is nil: a gemini_dir holds the user's conversations, history and
// credential. It is the engine's own data, not something ak generated, so it
// must never be reclaimed as an orphan the way codex profiles are.
func (agyEngine) ArtifactDirs(Context) []string { return nil }

// Launch selects the provider's gemini_dir and forces agy onto the credential
// file inside it. No key travels: agy signs in itself, and the token it writes
// into that directory is the only credential it reads back.
func (agyEngine) Launch(name string, p config.Provider, _ Secret, ctx Context) (Launch, error) {
	env := newEnv()
	env.set(EnvSSHConnection, AntigravitySSHConnection)
	env.set("AK_PROVIDER", name)
	// Extra env vars merge last, as for the other engines. One that names
	// SSH_CONNECTION is the user taking the switch over.
	for k, v := range p.Env {
		env.set(k, v)
	}

	args := AntigravityArgs(p)
	var variants []Variant
	for _, v := range mergeNames(config.EffortLevels(), p.Variants) {
		variants = append(variants, antigravityVariant(v, p))
	}
	return Launch{
		Env:            EnvPlan{Set: env.sorted()},
		Args:           args,
		Variants:       variants,
		DefaultVariant: p.DefaultVariant,
	}, nil
}

// AntigravityArgs renders the flags that select the provider's directory and
// model. --gemini_dir comes first so a user argument cannot shadow it.
func AntigravityArgs(p config.Provider) []string {
	args := []string{"--gemini_dir", config.ExpandHome(p.GeminiDir)}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	return args
}

// antigravityVariant turns a variant into agy flags: a built-in name is an
// effort level, a custom one may set the model, the effort, or both.
func antigravityVariant(name string, p config.Provider) Variant {
	custom, ok := p.Variants[name]
	if !ok {
		return Variant{Name: name, Args: []string{"--effort", name}}
	}
	v := Variant{Name: name, Env: sortedMap(custom.Env), Shim: custom.Shim}
	if custom.Model != "" {
		v.Args = append(v.Args, "--model", custom.Model)
	}
	if custom.Reasoning != "" {
		v.Args = append(v.Args, "--effort", custom.Reasoning)
	}
	return v
}
