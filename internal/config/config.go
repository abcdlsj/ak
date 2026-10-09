// Package config reads and writes ak's single source of truth,
// ~/.config/ak/providers.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Version is the config file format version.
const Version = 1

// DefaultGatewayAddr is where the pool gateway listens when settings.gateway_addr
// is not set. It is loopback-only: pool shims point their engine at it.
const DefaultGatewayAddr = "127.0.0.1:17877"

// DefaultMaxInflight is how many requests the gateway handles at once when
// settings.max_inflight is not set. It is generous: the point is to fail one
// caller fast rather than let a runaway one starve the machine.
const DefaultMaxInflight = 256

// Pool strategies decide which member a request goes to.
const (
	// StrategyOrder tries members in order, failing over to the next.
	StrategyOrder = "order"
	// StrategyRotate spreads requests over the members in turn.
	StrategyRotate = "rotate"
	// StrategyLeastUsed prefers the member that has served the fewest requests lately.
	StrategyLeastUsed = "least-used"
)

// Kind distinguishes the engine behind a provider.
type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
	KindPi     Kind = "pi"
)

// Config is the top-level structure of providers.toml.
type Config struct {
	Version   int                 `toml:"version"`
	Settings  Settings            `toml:"settings"`
	Providers map[string]Provider `toml:"providers"`
}

// Settings holds global settings.
type Settings struct {
	BinDir    string `toml:"bin_dir"`
	Prefix    string `toml:"prefix"`
	Default   string `toml:"default,omitempty"`
	ClaudeBin string `toml:"claude_bin,omitempty"`
	CodexBin  string `toml:"codex_bin,omitempty"`
	PiBin     string `toml:"pi_bin,omitempty"`
	// GatewayAddr is the listen address of the pool gateway, host:port.
	GatewayAddr string `toml:"gateway_addr,omitempty"`
	// MaxInflight bounds how many requests the gateway handles at once; over
	// it a request is refused with 503 rather than queued. 0 means no bound.
	MaxInflight int `toml:"max_inflight"`
}

// GatewayURL returns the loopback base URL of the pool gateway, without a
// trailing slash, e.g. http://127.0.0.1:17877.
func (s Settings) GatewayURL() string {
	addr := s.GatewayAddr
	if addr == "" {
		addr = DefaultGatewayAddr
	}
	return "http://" + addr
}

// Provider is a single provider. Shared by claude and codex; each ignores the
// fields that do not apply to it.
type Provider struct {
	Kind    Kind   `toml:"kind"`
	Display string `toml:"display,omitempty"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key,omitempty"`
	// APIKeyRef takes the form env:NAME / cmd:... / keychain:... and is
	// mutually exclusive with APIKey.
	APIKeyRef string `toml:"api_key_ref,omitempty"`
	Model     string `toml:"model,omitempty"`
	// DefaultVariant is applied when the provider launches without an explicit
	// variant, so the launcher does not ask each time: a pi thinking level, a
	// codex reasoning effort or a claude model tier. Empty keeps asking.
	DefaultVariant string `toml:"default_variant,omitempty"`

	// claude-only: three-tier model mapping. Empty values are derived from Model.
	Haiku  string `toml:"haiku,omitempty"`
	Sonnet string `toml:"sonnet,omitempty"`
	Opus   string `toml:"opus,omitempty"`
	// KeyField selects which environment variable receives the key:
	// auth_token (default) or api_key.
	KeyField string `toml:"key_field,omitempty"`
	// ConfigDir isolates the provider's whole claude environment: the shim
	// exports CLAUDE_CONFIG_DIR, so settings, login, MCP servers, plugins,
	// skills and sessions all live there instead of ~/.claude.
	ConfigDir string `toml:"config_dir,omitempty"`
	// Settings is a claude settings layer for this provider alone, passed as
	// --settings on top of the shared settings.json: a value here replaces the
	// shared one, everything else (login, sessions, plugins) stays shared.
	Settings map[string]any `toml:"settings,omitempty"`

	// codex-only.
	ProviderID string `toml:"provider_id,omitempty"`
	WireAPI    string `toml:"wire_api,omitempty"`
	Reasoning  string `toml:"reasoning,omitempty"`

	// pi-only. PiProvider names a provider pi already knows (its own config or
	// a package); when set, ak passes --provider through and writes nothing,
	// and base_url/key are unused. Otherwise ak registers an ak-<name> provider
	// in pi's models.json. PiAPI is the wire API for that entry:
	// anthropic-messages (default), openai-completions or openai-responses.
	PiProvider string `toml:"pi_provider,omitempty"`
	PiAPI      string `toml:"pi_api,omitempty"`
	// PiAuthHeader sends the key as Authorization: Bearer instead of the
	// Anthropic-style x-api-key, for relays that expect a bearer token.
	PiAuthHeader bool `toml:"pi_auth_header,omitempty"`
	// CodexHome isolates the provider's whole codex environment: the shim
	// exports CODEX_HOME, so config.toml, auth, MCP servers and sessions live
	// there instead of ~/.codex.
	CodexHome string `toml:"codex_home,omitempty"`

	// Members names, in order, the providers this provider routes over. When
	// non-empty the provider is a pool: BaseURL and the key fields are unused,
	// and its command points at the local gateway instead of an upstream.
	Members []string `toml:"members,omitempty"`
	// MemberModels maps a member name to the model the gateway asks that
	// member for, letting one pool map a logical model onto each upstream's
	// own name. A member with no entry gets the requested model unchanged.
	MemberModels map[string]string `toml:"member_models,omitempty"`
	// Strategy is how a request picks a member: order, rotate or least-used.
	// Empty means order.
	Strategy string `toml:"strategy,omitempty"`
	// MaxConcurrency bounds how many requests the pool gateway sends to this
	// provider at once; over it a request waits its turn. 0 means no bound.
	MaxConcurrency int `toml:"max_concurrency,omitempty"`

	// Env holds arbitrary extra environment variables. Merged last, it can
	// override derived keys.
	Env map[string]string `toml:"env,omitempty"`
	// Quota names the balance source to query: a built-in or quota.d plugin
	// id (deepseek, kimi, zhipu, newapi, …) or "off" to disable. Empty
	// auto-detects from the endpoint host.
	Quota string `toml:"quota,omitempty"`
	// QuotaCmd is a shell command that prints the balance as JSON (or a plain
	// number). It is asked with AK_QUOTA_KEY and AK_QUOTA_BASE_URL set, and
	// takes precedence over Quota, for vendors ak has no built-in for.
	QuotaCmd string `toml:"quota_cmd,omitempty"`
	// QuotaVars are values only the balance query reads, such as a relay's
	// access token and user id: a plugin names them as {{var.NAME}} and
	// quota_cmd gets them as AK_QUOTA_VAR_<NAME>. Unlike env they never reach
	// the engine's environment.
	QuotaVars map[string]string `toml:"quota_vars,omitempty"`
	// Pricing overrides the model pricing that models.dev cannot resolve.
	Pricing *Pricing `toml:"pricing,omitempty"`
	// Variants are the positional-argument variants of a command.
	Variants map[string]Variant `toml:"variants,omitempty"`
}

// Pricing is the price per million tokens in USD. When Discount is non-zero it
// acts as a multiplier against the models.dev price.
type Pricing struct {
	Discount   float64 `toml:"discount,omitempty"`
	Input      float64 `toml:"input,omitempty"`
	Output     float64 `toml:"output,omitempty"`
	CacheRead  float64 `toml:"cache_read,omitempty"`
	CacheWrite float64 `toml:"cache_write,omitempty"`
}

// Variant overrides the model or the reasoning effort.
type Variant struct {
	Model     string            `toml:"model,omitempty"`
	Reasoning string            `toml:"reasoning,omitempty"`
	Env       map[string]string `toml:"env,omitempty"`
	// Shim, when true, also generates a standalone command
	// ak-<provider>-<variant>.
	Shim bool `toml:"shim,omitempty"`
}

// IsPool reports whether the provider routes over other providers rather
// than talking to an upstream itself.
func (p Provider) IsPool() bool { return len(p.Members) > 0 }

// StrategyOrDefault returns the pool's strategy, defaulting to order.
func (p Provider) StrategyOrDefault() string {
	if p.Strategy == "" {
		return StrategyOrder
	}
	return p.Strategy
}

// Names returns the sorted provider names, keeping every iteration
// deterministic.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Dir returns the config directory, ~/.config/ak.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ak"), nil
}

// Path returns the absolute path of providers.toml.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "providers.toml"), nil
}

// DataDir returns ~/.local/share/ak, which holds the usage aggregates and the
// session attribution index.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ak"), nil
}

// Default returns an empty config pre-filled with defaults.
func Default() *Config {
	return &Config{
		Version:   Version,
		Settings:  Settings{BinDir: "~/.local/bin", Prefix: "ak-", GatewayAddr: DefaultGatewayAddr, MaxInflight: DefaultMaxInflight},
		Providers: map[string]Provider{},
	}
}

// Load reads the config. A missing file returns the default config rather than
// an error, so the first run works out of the box.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	if cfg.Settings.BinDir == "" {
		cfg.Settings.BinDir = "~/.local/bin"
	}
	if cfg.Settings.Prefix == "" {
		cfg.Settings.Prefix = "ak-"
	}
	if cfg.Settings.GatewayAddr == "" {
		cfg.Settings.GatewayAddr = DefaultGatewayAddr
	}
	return cfg, nil
}

// UnknownKeys lists the keys in providers.toml that ak does not know, such as
// a misspelt field. Load ignores them, and the next save drops them.
func UnknownKeys() ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var strict *toml.StrictMissingError
	if err := dec.Decode(Default()); !errors.As(err, &strict) {
		return nil, nil
	}
	var keys []string
	for _, e := range strict.Errors {
		keys = append(keys, strings.Join(e.Key(), "."))
	}
	return keys, nil
}

// Save writes the config back atomically with mode 0600.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

// atomicWrite writes a temp file in the same directory and renames it, so an
// interruption never leaves a truncated file behind.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".ak-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AtomicWrite is the shared atomic write used by other packages.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	return atomicWrite(path, data, mode)
}

// ExpandHome expands a leading ~ into the user's home directory.
func ExpandHome(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if len(p) > 1 && (p[1] == '/' || p[1] == filepath.Separator) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// keyRefPrefixes mark an api key value as a reference rather than a key.
var keyRefPrefixes = []string{"env:", "cmd:", "keychain:"}

// IsKeyRef reports whether s is an api_key_ref (env:NAME, cmd:..., keychain:...).
func IsKeyRef(s string) bool {
	for _, p := range keyRefPrefixes {
		if len(s) > len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

// SetKey stores a key or a key reference, clearing the other.
func (p *Provider) SetKey(v string) {
	if IsKeyRef(v) {
		p.APIKey, p.APIKeyRef = "", v
	} else {
		p.APIKey, p.APIKeyRef = v, ""
	}
}
