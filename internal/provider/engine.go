package provider

import (
	"sort"

	"github.com/abcdlsj/ak/internal/config"
)

// Engine turns a configured provider into everything its command needs.
// Adding an engine means implementing Engine and listing it in engines; the
// shim package renders and reclaims whatever an Engine describes.
type Engine interface {
	Kind() config.Kind
	// BinName is the engine's command name, used as the PATH fallback.
	BinName() string
	// BinEnvVar overrides the pinned binary at run time, e.g. AK_CLAUDE_BIN.
	BinEnvVar() string
	// ConfiguredBin is the binary pinned in settings, if any.
	ConfiguredBin(s config.Settings) string
	// Launch describes the command for one provider.
	Launch(name string, p config.Provider, key Secret, ctx Context) (Launch, error)
	// ArtifactDirs lists directories where this engine's extra files live, so
	// stale ones are reclaimed even after the last provider of the kind is gone.
	ArtifactDirs(ctx Context) []string
}

// Context carries machine-specific locations, kept out of the engines so
// tests stay hermetic.
type Context struct {
	CodexHome string
}

// Secret is how a command obtains the provider's key.
type Secret struct {
	// Value is the plaintext key, written into the command.
	Value string
	// Deferred means the key is resolved each time the command runs
	// (api_key_ref), so no plaintext copy is written to disk.
	Deferred bool
}

// Literal wraps a plaintext key.
func Literal(v string) Secret { return Secret{Value: v} }

// Launch is everything a provider's command does.
type Launch struct {
	Env EnvPlan
	// Args are passed to the engine before the user's arguments.
	Args []string
	// Variants are recognised as the command's first argument.
	Variants []Variant
	// Files are extra artifacts, e.g. a codex profile.
	Files []File
}

// Variant is a named adjustment applied on top of the base launch.
type Variant struct {
	Name string
	Env  []KV
	Args []string
	// Shim also generates a standalone command <prefix><provider>-<variant>.
	Shim bool
}

// File is an extra artifact. The shim package adds the marker line.
type File struct {
	Path string
	Body []byte
}

// KV is an ordered environment variable key-value pair.
type KV struct {
	Key   string
	Value string
	// Secret marks the provider key; with a deferred Secret, Value is empty
	// and the command fills it in at run time.
	Secret bool
}

// EnvPlan is the full environment a shim injects: Set is exported, Unset is
// unset.
type EnvPlan struct {
	Set   []KV
	Unset []string
}

// Deferred reports whether the plan needs the key resolved at run time.
func (p EnvPlan) Deferred() bool {
	for _, kv := range p.Set {
		if kv.Secret && kv.Value == "" {
			return true
		}
	}
	return false
}

var engines = []Engine{claudeEngine{}, codexEngine{}}

// Engines lists every supported engine.
func Engines() []Engine { return engines }

// EngineFor returns the engine for a kind, or nil when unsupported.
func EngineFor(k config.Kind) Engine {
	for _, e := range engines {
		if e.Kind() == k {
			return e
		}
	}
	return nil
}

// envBuilder accumulates an environment, keeping the secret key marked.
type envBuilder struct {
	vals   map[string]string
	secret string
}

func newEnv() *envBuilder { return &envBuilder{vals: map[string]string{}} }

func (b *envBuilder) set(k, v string) { b.vals[k] = v }

func (b *envBuilder) setIf(k, v string) {
	if v != "" {
		b.vals[k] = v
	}
}

// setSecret records the key variable. A deferred secret has no value yet but
// still counts as set, so it is never unset.
func (b *envBuilder) setSecret(k string, s Secret) {
	if s.Value == "" && !s.Deferred {
		return
	}
	b.vals[k] = s.Value
	b.secret = k
}

func (b *envBuilder) has(k string) bool {
	_, ok := b.vals[k]
	return ok
}

// sorted returns the set list in key order, skipping the given keys.
func (b *envBuilder) sorted(skip ...string) []KV {
	drop := map[string]bool{}
	for _, k := range skip {
		drop[k] = true
	}
	keys := make([]string, 0, len(b.vals))
	for k := range b.vals {
		if !drop[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]KV, 0, len(keys))
	for _, k := range keys {
		out = append(out, KV{Key: k, Value: b.vals[k], Secret: k == b.secret})
	}
	return out
}

func sortedMap(m map[string]string) []KV {
	b := newEnv()
	for k, v := range m {
		b.set(k, v)
	}
	return b.sorted()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
