// Package config reads and writes ak's single source of truth,
// ~/.config/ak/providers.toml.
package config

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/vendor"
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

// DefaultMaxConcurrency is how many requests one member takes at once when
// neither it nor the settings say. An agent fires several turns at once; a
// vendor that sees more than this tends to answer 429.
const DefaultMaxConcurrency = 16

// Pool strategies decide which member a request goes to.
const (
	// StrategyOrder tries members in order, failing over to the next.
	StrategyOrder = "order"
	// StrategyRotate spreads requests over the members in turn.
	StrategyRotate = "rotate"
	// StrategyLeastUsed prefers the member that has served the fewest requests lately.
	StrategyLeastUsed = "least-used"
	// StrategySmart prefers, among members with allowance left, the one whose
	// plan window resets soonest, so the least of it goes unused.
	StrategySmart = "smart"
)

// Kind distinguishes the engine behind a provider.
type Kind string

const (
	KindClaude      Kind = "claude"
	KindCodex       Kind = "codex"
	KindPi          Kind = "pi"
	KindAntigravity Kind = "antigravity"
)

// Config is the top-level structure of providers.toml.
type Config struct {
	Version   int                 `toml:"version"`
	Settings  Settings            `toml:"settings"`
	Accounts  map[string]Account  `toml:"accounts,omitempty"`
	Providers map[string]Provider `toml:"providers"`
	// Service is what `ak serve` answers other machines with.
	Service Service `toml:"service,omitempty"`
}

// Settings holds global settings.
type Settings struct {
	BinDir  string `toml:"bin_dir"`
	Prefix  string `toml:"prefix"`
	Default string `toml:"default,omitempty"`
	// DefaultClaude, DefaultCodex, DefaultPi and DefaultAntigravity are what
	// the engine's own command (claude, codex, pi, agy) runs on through `ak
	// init`'s shell functions where no binding applies: a provider[:variant] of
	// that kind. Empty keeps the engine's own command.
	DefaultClaude      string `toml:"default_claude,omitempty"`
	DefaultCodex       string `toml:"default_codex,omitempty"`
	DefaultPi          string `toml:"default_pi,omitempty"`
	DefaultAntigravity string `toml:"default_antigravity,omitempty"`
	// ClaudeArgs, CodexArgs, PiArgs and AntigravityArgs are put in front of the
	// user's own arguments on every launch of that engine through ak: a
	// provider, the shell functions of ak init, natively or not.
	ClaudeArgs      []string `toml:"claude_args,omitempty"`
	CodexArgs       []string `toml:"codex_args,omitempty"`
	PiArgs          []string `toml:"pi_args,omitempty"`
	AntigravityArgs []string `toml:"antigravity_args,omitempty"`
	ClaudeBin       string   `toml:"claude_bin,omitempty"`
	CodexBin        string   `toml:"codex_bin,omitempty"`
	PiBin           string   `toml:"pi_bin,omitempty"`
	AntigravityBin  string   `toml:"antigravity_bin,omitempty"`
	// GatewayAddr is the listen address of the pool gateway, host:port.
	GatewayAddr string `toml:"gateway_addr,omitempty"`
	// MaxInflight bounds how many requests the gateway handles at once; over
	// it a request is refused with 503 rather than queued. 0 means no bound.
	MaxInflight int `toml:"max_inflight"`
	// MaxConcurrency is how many requests one member takes at once when it
	// names no bound of its own; extra requests queue rather than reach a
	// vendor that would answer them with a 429. 0 means no bound.
	MaxConcurrency int `toml:"max_concurrency"`
}

// Kinds are the engines, in display order.
var Kinds = []Kind{KindClaude, KindCodex, KindPi, KindAntigravity}

// EngineDefault returns the target the engine's own command runs on where no
// binding applies, or "".
func (s Settings) EngineDefault(k Kind) string {
	switch k {
	case KindClaude:
		return s.DefaultClaude
	case KindCodex:
		return s.DefaultCodex
	case KindPi:
		return s.DefaultPi
	case KindAntigravity:
		return s.DefaultAntigravity
	}
	return ""
}

// EngineArgs returns the arguments every launch of engine k gets.
func (s Settings) EngineArgs(k Kind) []string {
	switch k {
	case KindClaude:
		return s.ClaudeArgs
	case KindCodex:
		return s.CodexArgs
	case KindPi:
		return s.PiArgs
	case KindAntigravity:
		return s.AntigravityArgs
	}
	return nil
}

// EngineDefaultPtr returns the field holding k's engine default, or nil.
func (s *Settings) EngineDefaultPtr(k Kind) *string {
	switch k {
	case KindClaude:
		return &s.DefaultClaude
	case KindCodex:
		return &s.DefaultCodex
	case KindPi:
		return &s.DefaultPi
	case KindAntigravity:
		return &s.DefaultAntigravity
	}
	return nil
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
	// Account names the account the provider runs on. Its key is the
	// account's, and each endpoint field left unset is the account vendor's
	// for this engine (see Resolve).
	Account string `toml:"account,omitempty"`
	BaseURL string `toml:"base_url,omitempty"`
	// APIKey is the plaintext key. It is never written to providers.toml:
	// Save stores it in auth.json under AuthKey, and Load reads it back.
	APIKey string `toml:"-"`
	// AuthKey names the provider's entry in auth.json. Save sets it to the
	// provider's name when empty; providers naming one entry share its key.
	AuthKey string `toml:"auth_key,omitempty"`
	// APIKeyRef takes the form env:NAME / cmd:... / keychain:... and is
	// mutually exclusive with APIKey.
	APIKeyRef string `toml:"api_key_ref,omitempty"`
	// Secrets are the account's further credentials, filled by Resolve and
	// never written.
	Secrets map[string]string `toml:"-"`
	Model   string            `toml:"model,omitempty"`
	// ContextWindow and MaxTokens are the model's context window and the
	// longest reply it may write, in tokens. Every engine keeps these two
	// numbers in a file or a variable of its own and guesses badly without
	// them, so ak hands over what it knows: these win over what models.dev
	// lists for the model, and 0 means take models.dev's, else leave the
	// engine its own default.
	ContextWindow int `toml:"context_window,omitempty"`
	MaxTokens     int `toml:"max_tokens,omitempty"`
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
	// ModelCatalog is a codex model catalog JSON this provider loads as
	// -c model_catalog_json, giving a model codex has no metadata for its
	// context window and reasoning levels. Codex replaces its whole built-in
	// catalog with the file, so set it only where the provider's model needs
	// it. Empty keeps the built-in catalog.
	ModelCatalog string `toml:"model_catalog,omitempty"`

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

	// antigravity-only. GeminiDir isolates the provider's whole agy
	// environment: the launch passes it as --gemini_dir, so config (hooks, MCP
	// servers, skills, plugins, projects), conversations, settings, logs and
	// the OAuth credential all live there instead of ~/.gemini. Because the
	// credential travels with the directory, one provider is one Antigravity
	// account, and providers run side by side. Required: an empty value would
	// silently put the launch back on ~/.gemini.
	GeminiDir string `toml:"gemini_dir,omitempty"`

	// Members names, in order, what this provider routes over. When non-empty
	// the provider is a pool: BaseURL and the key fields are unused, and its
	// command points at the local gateway instead of an upstream. A member is
	// a provider, an account, or an account and the model to ask it for as
	// <account>/<model>.
	Members []string `toml:"members,omitempty"`
	// implicit marks a provider ak made for a member that named an account: it
	// carries the account's endpoint and key for the member's model. It is
	// never written, never gets a command and never appears in a list; delete
	// it and the next Resolve makes it again.
	implicit bool
	// MemberModels maps a member name to the model the gateway asks that
	// member for, letting one pool map a logical model onto each upstream's
	// own name. A member with no entry is asked for its own model, and a
	// member that names none gets the requested model unchanged.
	MemberModels map[string]string `toml:"member_models,omitempty"`
	// Strategy is how a request picks a member: order, rotate, least-used or
	// smart. A member may itself be a pool, which picks among its own.
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
	// Variants are the named variants a launch selects with <provider>:<variant>.
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

// MaxPoolDepth bounds how deeply pools may nest.
const MaxPoolDepth = 4

// Leaves expands a provider into the concrete providers it routes to, in
// member order: itself when it is not a pool, else its members' leaves, each
// once. A missing member or a cycle is skipped; Validate rejects both.
func (c *Config) Leaves(name string) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(n string, depth int, path map[string]bool)
	walk = func(n string, depth int, path map[string]bool) {
		p, ok := c.Providers[n]
		if !ok || path[n] || depth > MaxPoolDepth {
			return
		}
		if !p.IsPool() {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
			return
		}
		path[n] = true
		for _, m := range p.Members {
			walk(m, depth+1, path)
		}
		delete(path, n)
	}
	walk(name, 0, map[string]bool{})
	return out
}

// Refs expands a provider into the accounts and models it answers with, in
// the order it would answer: a plain provider gives its own account and
// model, a pool its members', in member order. A member is asked for the
// pool's mapping of it, else the model it was set up with, else the model
// the name above it means, so the order here is the order the gateway uses.
//
// A provider with an endpoint of its own is no account's and gives nothing:
// the service routes to accounts.
func (c *Config) Refs(name string) []ModelRef {
	seen := map[string]bool{}
	var out []ModelRef
	var walk func(name, asked, inherited string, depth int)
	walk = func(name, asked, inherited string, depth int) {
		p, ok := c.Providers[name]
		if !ok {
			// A pool's member may be an account of its own: it answers with
			// the model the pool asks it for, or the one its plan serves.
			if _, isAccount := c.Accounts[name]; isAccount {
				if m := cmp.Or(asked, inherited); !seen[name+"\x00"+m] {
					seen[name+"\x00"+m] = true
					out = append(out, ModelRef{Account: name, Model: m})
				}
			}
			return
		}
		if depth > MaxPoolDepth {
			return
		}
		if !p.IsPool() {
			if p.Account == "" {
				return
			}
			m := cmp.Or(asked, p.Model, inherited)
			if seen[p.Account+"\x00"+m] {
				return
			}
			seen[p.Account+"\x00"+m] = true
			out = append(out, ModelRef{Account: p.Account, Model: m})
			return
		}
		for _, member := range p.Members {
			walk(member, p.MemberModels[member], cmp.Or(p.Model, inherited), depth+1)
		}
	}
	walk(name, "", "", 0)
	return out
}

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
	for n, p := range c.Providers {
		if p.implicit {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DirEnv names the variable that relocates the config directory. A process
// whose HOME is not the user's, such as an ACP adapter a client starts in a
// sandboxed home, sets it to reach the user's providers.
const DirEnv = "AK_CONFIG_DIR"

// Dir returns the config directory: $AK_CONFIG_DIR, else ~/.config/ak.
func Dir() (string, error) {
	if dir := os.Getenv(DirEnv); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", DirEnv, dir)
		}
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ak"), nil
}

// PiHomeEnv is the variable pi reads to relocate its agent directory.
const PiHomeEnv = "PI_CODING_AGENT_DIR"

// PiHome is pi's agent directory, where ak keeps its models.json entries:
// $PI_CODING_AGENT_DIR when that is set, else ~/.pi/agent — or, when the config
// directory is relocated (AK_CONFIG_DIR), a directory inside it. A run against
// a config of its own writes nothing outside that config, so a sandbox is a
// sandbox and a test cannot reach into the real home.
func PiHome() (string, error) {
	if v := os.Getenv(PiHomeEnv); v != "" {
		return ExpandHome(v), nil
	}
	if os.Getenv(DirEnv) != "" {
		dir, err := Dir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "pi"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

// PiHomeSandboxed reports whether PiHome is ak's own rather than the user's,
// which pi has to be told at launch to read it.
func PiHomeSandboxed() bool {
	return os.Getenv(PiHomeEnv) == "" && os.Getenv(DirEnv) != ""
}

// Path returns the absolute path of providers.toml.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "providers.toml"), nil
}

// AuthPath returns the absolute path of auth.json, which holds the plaintext
// keys providers.toml refers to by auth_key.
func AuthPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "auth.json"), nil
}

// DataDir returns the directory holding ak's state (usage aggregates, the
// session attribution index, the launcher's directory record, the gateway's
// log). It is the config directory: everything ak keeps is in one place.
func DataDir() (string, error) { return Dir() }

// Default returns an empty config pre-filled with defaults.
func Default() *Config {
	return &Config{
		Version: Version,
		Settings: Settings{BinDir: "~/.local/bin", Prefix: "ak-", GatewayAddr: DefaultGatewayAddr,
			MaxInflight: DefaultMaxInflight, MaxConcurrency: DefaultMaxConcurrency},
		Accounts:  map[string]Account{},
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
	// The user's vendors first: accounts and providers resolve against them.
	vendor.LoadDir(filepath.Join(filepath.Dir(path), "vendors.d"))
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
	if cfg.Version > Version {
		return nil, fmt.Errorf("%s has format version %d, newer than this ak reads (%d); upgrade ak", path, cfg.Version, Version)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	if cfg.Accounts == nil {
		cfg.Accounts = map[string]Account{}
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
	auth, err := loadAuth()
	if err != nil {
		return nil, err
	}
	// A missing entry leaves the key empty, which health reports as no key.
	for name, p := range cfg.Providers {
		if p.AuthKey != "" {
			p.APIKey = auth[p.AuthKey]
			cfg.Providers[name] = p
		}
	}
	cfg.readAccountAuth(auth)
	cfg.Resolve()
	return cfg, nil
}

// loadAuth reads auth.json; a missing file holds no keys.
func loadAuth() (map[string]string, error) {
	path, err := AuthPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	auth := map[string]string{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return auth, nil
}

// authEntries assigns every provider holding a key of its own its auth_key
// and returns the keys by entry, the accounts' among them. Two providers
// naming one entry must hold the same key.
func authEntries(cfg *Config) (map[string]string, error) {
	auth := map[string]string{}
	owner := map[string]string{}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.APIKey == "" || p.Account != "" {
			continue
		}
		if p.AuthKey == "" {
			p.AuthKey = name
			cfg.Providers[name] = p
		}
		if prev, ok := auth[p.AuthKey]; ok && prev != p.APIKey {
			// A provider whose key was changed apart from the others sharing
			// the entry moves to an entry of its own.
			if prev, ok := auth[name]; ok && prev != p.APIKey {
				return nil, fmt.Errorf("providers %q and %q share auth_key %q but hold different keys", owner[p.AuthKey], name, p.AuthKey)
			}
			p.AuthKey = name
			cfg.Providers[name] = p
		}
		auth[p.AuthKey] = p.APIKey
		owner[p.AuthKey] = name
	}
	cfg.writeAccountAuth(auth)
	return auth, nil
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

// Save writes the config back atomically with mode 0600: the keys to
// auth.json, everything else to providers.toml, each provider as Stored
// gives it. auth.json is rewritten whole, so an entry no provider or account
// names any more is dropped.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	authPath, err := AuthPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	auth, err := authEntries(cfg)
	if err != nil {
		return err
	}
	authData, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	// Keys first: a providers.toml naming an entry auth.json lacks would
	// launch with no key.
	if err := atomicWrite(authPath, append(authData, '\n'), 0o600); err != nil {
		return err
	}
	stored := *cfg
	stored.Providers = make(map[string]Provider, len(cfg.Providers))
	for name, p := range cfg.Providers {
		if p.implicit {
			continue // ak made it for a member; the member is what is written
		}
		stored.Providers[name] = cfg.Stored(p)
	}
	// A route that names a provider keeps naming it: its members are the
	// provider's, and were only filled in to be routed by.
	stored.Service.Routes = maps.Clone(stored.Service.Routes)
	stored.collapseRoutes()
	data, err := toml.Marshal(&stored)
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
		p.APIKey, p.APIKeyRef, p.AuthKey = "", v, ""
	} else {
		p.APIKey, p.APIKeyRef = v, ""
	}
}
