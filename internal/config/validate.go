package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// nameRe constrains provider names so they are safe as a command name suffix.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Reserved holds ak's own subcommand names. A provider must not claim one,
// otherwise `ak <name>` becomes ambiguous.
var Reserved = map[string]bool{
	"ui": true, "pick": true, "list": true, "ls": true, "add": true,
	"rm": true, "remove": true, "edit": true, "sync": true, "doctor": true,
	"import": true, "default": true, "env": true, "usage": true,
	"completion": true, "help": true, "version": true, "run": true,
	"prune-codexa": true, "rename": true, "mv": true, "serve": true,
	"quota": true, "preset": true, "check": true, "models": true,
}

// claudeSubcommands and codexSubcommands are the banned variant names.
// A variant is matched against the first positional argument, so a variant
// named like one of the engine's own subcommands would swallow it and make
// that subcommand unreachable.
var claudeSubcommands = map[string]bool{
	"mcp": true, "update": true, "doctor": true, "config": true,
	"install": true, "setup-token": true, "migrate-installer": true,
	"resume": true, "plugin": true,
}

var codexSubcommands = map[string]bool{
	"exec": true, "login": true, "logout": true, "mcp": true, "review": true,
	"resume": true, "apply": true, "completion": true, "debug": true,
	"doctor": true, "queue": true, "archive": true, "delete": true,
	"unarchive": true, "fork": true, "sandbox": true, "cloud": true,
}

// ValidateName validates a provider name.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid provider name %q: only lowercase letters, digits, dots, underscores and hyphens are allowed, and it must start with a letter or digit", name)
	}
	if Reserved[name] {
		return fmt.Errorf("provider name %q collides with an ak subcommand", name)
	}
	return nil
}

// Validate validates the whole config.
func Validate(cfg *Config) error {
	if cfg.Settings.Prefix == "" {
		return fmt.Errorf("settings.prefix must not be empty")
	}
	if cfg.Settings.MaxInflight < 0 {
		return fmt.Errorf("settings.max_inflight must not be negative")
	}
	for _, name := range cfg.Names() {
		if err := ValidateName(name); err != nil {
			return err
		}
		p := cfg.Providers[name]
		if err := validateProvider(cfg, name, p); err != nil {
			return err
		}
	}
	if err := validateCommandNames(cfg); err != nil {
		return err
	}
	if d := cfg.Settings.Default; d != "" {
		if _, ok := cfg.Providers[d]; !ok {
			return fmt.Errorf("settings.default points at unknown provider %q", d)
		}
	}
	return nil
}

// validateCommandNames rejects a standalone variant command that would share
// its name with another provider's command: ak-foo-bar is both provider
// foo's variant bar and provider foo-bar.
func validateCommandNames(cfg *Config) error {
	owner := map[string]string{}
	for _, name := range cfg.Names() {
		owner[name] = "provider " + name
	}
	for _, name := range cfg.Names() {
		for v, variant := range cfg.Providers[name].Variants {
			if !variant.Shim {
				continue
			}
			cmd := name + "-" + v
			if o, taken := owner[cmd]; taken {
				return fmt.Errorf("provider %q variant %q would generate %s%s, already used by %s",
					name, v, cfg.Settings.Prefix, cmd, o)
			}
			owner[cmd] = fmt.Sprintf("provider %s variant %s", name, v)
		}
	}
	return nil
}

func validateProvider(cfg *Config, name string, p Provider) error {
	if p.MaxConcurrency < 0 {
		return fmt.Errorf("provider %q has negative max_concurrency", name)
	}
	switch p.Kind {
	case KindClaude, KindCodex, KindPi:
	case "":
		return fmt.Errorf("provider %q is missing kind (claude, codex or pi)", name)
	default:
		return fmt.Errorf("provider %q has invalid kind %q; must be claude, codex or pi", name, p.Kind)
	}
	if err := validateEnv(name, p); err != nil {
		return err
	}
	if err := validateIsolation(name, p); err != nil {
		return err
	}
	if p.Kind == KindCodex {
		if err := validateCodex(name, p); err != nil {
			return err
		}
	}
	if p.IsPool() {
		if err := validateQuota(name, p.Quota); err != nil {
			return err
		}
		// A pi pool is registered in models.json like any other pi provider,
		// so its pi_api must be valid too.
		if p.Kind == KindPi {
			if err := validatePi(name, p); err != nil {
				return err
			}
		}
		return validatePool(cfg, name, p)
	}
	if p.BaseURL == "" && p.PiProvider == "" {
		return fmt.Errorf("provider %q is missing base_url", name)
	}
	if p.APIKey != "" && p.APIKeyRef != "" {
		return fmt.Errorf("provider %q sets both api_key and api_key_ref; only one is allowed", name)
	}
	if p.Kind == KindClaude {
		switch p.KeyField {
		case "", "auth_token", "api_key":
		default:
			return fmt.Errorf("provider %q has invalid key_field %q; must be auth_token or api_key", name, p.KeyField)
		}
	}
	if p.Kind == KindPi {
		if err := validatePi(name, p); err != nil {
			return err
		}
	}
	if err := validateQuota(name, p.Quota); err != nil {
		return err
	}
	return validateVariants(name, p)
}

func validateCodex(name string, p Provider) error {
	switch p.WireAPI {
	case "", "responses", "chat":
	default:
		return fmt.Errorf("provider %q has invalid wire_api %q; must be responses or chat", name, p.WireAPI)
	}
	if r := p.Reasoning; r != "" && !validReasoning[r] {
		return fmt.Errorf("provider %q has invalid reasoning %q", name, r)
	}
	return nil
}

// envKeyRe is a shell variable name: anything else would break, or inject
// into, the generated command.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateEnv(name string, p Provider) error {
	for k := range p.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("provider %q has invalid env name %q; use letters, digits and _", name, k)
		}
	}
	for k := range p.QuotaVars {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("provider %q has invalid quota_vars name %q; use letters, digits and _", name, k)
		}
	}
	for v, variant := range p.Variants {
		for k := range variant.Env {
			if !envKeyRe.MatchString(k) {
				return fmt.Errorf("provider %q variant %q has invalid env name %q; use letters, digits and _", name, v, k)
			}
		}
	}
	return nil
}

// ClaudeRoutingKeys are the environment variables that pick claude's
// endpoint, key and models. Claude applies a settings file's env over the
// process environment, so any of these in a settings env silently overrides
// what an ak command sets.
var ClaudeRoutingKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
}

// validateIsolation checks each engine's isolation fields are its own, and
// that the settings layer can be passed to claude as JSON.
func validateIsolation(name string, p Provider) error {
	if p.ConfigDir != "" && p.Kind != KindClaude {
		return fmt.Errorf("provider %q sets config_dir, which only applies to claude (codex uses codex_home)", name)
	}
	if p.CodexHome != "" && p.Kind != KindCodex {
		return fmt.Errorf("provider %q sets codex_home, which only applies to codex (claude uses config_dir)", name)
	}
	if len(p.Settings) > 0 {
		if p.Kind != KindClaude {
			return fmt.Errorf("provider %q sets settings, which only applies to claude", name)
		}
		if _, err := json.Marshal(p.Settings); err != nil {
			return fmt.Errorf("provider %q settings cannot be passed to claude as JSON: %v", name, err)
		}
		// claude applies a settings env over the process environment, so a
		// routing key here would override the one the command sets.
		if env, ok := p.Settings["env"].(map[string]any); ok {
			for _, k := range ClaudeRoutingKeys {
				if _, set := env[k]; set {
					return fmt.Errorf("provider %q sets %s in settings.env, which would override its own endpoint; set it through the provider's fields or env instead", name, k)
				}
			}
		}
	}
	return nil
}

// wireOf is the protocol a provider speaks to its upstream, defaults filled
// in, so a pool can require its members to speak the same.
func wireOf(p Provider) string {
	switch p.Kind {
	case KindCodex:
		if p.WireAPI == "" {
			return "responses"
		}
		return p.WireAPI
	case KindPi:
		if p.PiAPI == "" {
			return "anthropic-messages"
		}
		return p.PiAPI
	}
	return ""
}

// quotaRe constrains a quota source id; unknown ids are caught with a
// clear message when the source is resolved, not here.
var quotaRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func validateQuota(name, q string) error {
	if q == "" || q == "off" {
		return nil
	}
	if !quotaRe.MatchString(q) {
		return fmt.Errorf("provider %q has invalid quota %q; use a source id or \"off\"", name, q)
	}
	return nil
}

// validatePool checks a routing pool: its members exist, share its kind and
// wire, do not lead back to it and nest no deeper than MaxPoolDepth, and its
// strategy and model mapping name members.
func validatePool(cfg *Config, name string, p Provider) error {
	switch p.Strategy {
	case "", StrategyOrder, StrategyRotate, StrategyLeastUsed, StrategySmart:
	default:
		return fmt.Errorf("provider %q has invalid strategy %q; must be order, rotate, least-used or smart", name, p.Strategy)
	}
	if err := poolDepth(cfg, name, nil); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range p.Members {
		if m == name {
			return fmt.Errorf("pool %q lists itself as a member", name)
		}
		if seen[m] {
			return fmt.Errorf("pool %q lists member %q twice", name, m)
		}
		seen[m] = true
		mp, ok := cfg.Providers[m]
		if !ok {
			return fmt.Errorf("pool %q names unknown member %q", name, m)
		}
		if mp.Kind != p.Kind {
			return fmt.Errorf("pool %q is %s but member %q is %s; a pool's members must share its kind", name, p.Kind, m, mp.Kind)
		}
		if mp.BaseURL == "" && !mp.IsPool() {
			return fmt.Errorf("pool %q names %q, which has no base_url (a pi provider that uses pi_provider); a pool member needs an endpoint", name, m)
		}
		if w, mw := wireOf(p), wireOf(mp); w != mw {
			return fmt.Errorf("pool %q speaks %s but member %q speaks %s; pools do not translate between protocols", name, w, m, mw)
		}
	}
	for m := range p.MemberModels {
		if !seen[m] {
			return fmt.Errorf("pool %q maps a model for %q, which is not one of its members", name, m)
		}
	}
	// A pi pool is registered in models.json and needs a model for pi to pick.
	if p.Kind == KindPi && p.Model == "" {
		return fmt.Errorf("pi pool %q needs a model; set model to the name to request", name)
	}
	return validateVariants(name, p)
}

// poolDepth rejects a pool whose members lead back to it, or that nests
// deeper than MaxPoolDepth.
func poolDepth(cfg *Config, name string, path []string) error {
	for _, n := range path {
		if n == name {
			return fmt.Errorf("pool %q leads back to itself: %s", path[0], strings.Join(append(path, name), " → "))
		}
	}
	p := cfg.Providers[name]
	if !p.IsPool() {
		return nil
	}
	path = append(path, name)
	if len(path) > MaxPoolDepth {
		return fmt.Errorf("pool %q nests deeper than %d: %s", path[0], MaxPoolDepth, strings.Join(path, " → "))
	}
	for _, m := range p.Members {
		if err := poolDepth(cfg, m, path); err != nil {
			return err
		}
	}
	return nil
}

func validateVariants(name string, p Provider) error {
	var banned map[string]bool
	switch p.Kind {
	case KindCodex:
		banned = codexSubcommands
	case KindClaude:
		banned = claudeSubcommands
	}
	if p.DefaultVariant != "" && !validVariant(p, p.DefaultVariant) {
		return fmt.Errorf("provider %q has default_variant %q, which is not a %s variant", name, p.DefaultVariant, p.Kind)
	}
	for v := range p.Variants {
		if !nameRe.MatchString(v) {
			return fmt.Errorf("provider %q has invalid variant name %q", name, v)
		}
		if banned[v] {
			return fmt.Errorf("provider %q variant %q collides with a %s subcommand, making that subcommand unreachable", name, v, p.Kind)
		}
		switch {
		case p.Kind == KindClaude && implicitClaudeVariants[v]:
			return fmt.Errorf("provider %q variant %q collides with a built-in model tier variant", name, v)
		case p.Kind == KindCodex && validReasoning[v]:
			return fmt.Errorf("provider %q variant %q collides with a reasoning effort level", name, v)
		case p.Kind == KindPi && validThinking[v]:
			return fmt.Errorf("provider %q variant %q collides with a built-in thinking level", name, v)
		}
	}
	return nil
}

// validVariant reports whether v names a variant the provider's command
// recognises: a custom variant, or a built-in level or tier of its kind.
func validVariant(p Provider, v string) bool {
	if _, ok := p.Variants[v]; ok {
		return true
	}
	switch p.Kind {
	case KindClaude:
		return implicitClaudeVariants[v]
	case KindCodex:
		return validReasoning[v]
	case KindPi:
		return validThinking[v]
	}
	return false
}

// validatePi checks a pi provider: its wire API, and that ak-managed ones name
// at least one model to register.
func validatePi(name string, p Provider) error {
	switch p.PiAPI {
	case "", "anthropic-messages", "openai-completions", "openai-responses":
	default:
		return fmt.Errorf("provider %q has invalid pi_api %q; must be anthropic-messages, openai-completions or openai-responses", name, p.PiAPI)
	}
	if p.PiProvider != "" {
		return nil
	}
	if p.Model == "" {
		for _, v := range p.Variants {
			if v.Model != "" {
				return nil
			}
		}
		return fmt.Errorf("provider %q is a pi provider with no model; set model (or pi_provider) so ak can register it", name)
	}
	return nil
}

// validReasoning lists codex's reasoning effort levels. They double as the
// implicit variants of a codex shim.
var validReasoning = map[string]bool{
	"minimal": true, "low": true, "medium": true, "high": true,
	"xhigh": true, "max": true,
}

// validThinking lists pi's thinking levels. They double as the implicit
// variants of a pi shim, each adding --thinking <level>.
var validThinking = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// implicitClaudeVariants lists the built-in model tier variants of a claude
// shim.
var implicitClaudeVariants = map[string]bool{
	"opus": true, "sonnet": true, "haiku": true,
}

// reasoningLevels and claudeTiers are the same sets in their natural order,
// weakest first, for anything shown to a person.
var (
	reasoningLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	claudeTiers     = []string{"opus", "sonnet", "haiku"}
)

// ReasoningLevels lists codex reasoning efforts, weakest first.
func ReasoningLevels() []string { return reasoningLevels }

// ThinkingLevels lists pi thinking levels, weakest first.
func ThinkingLevels() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
}

// ValidThinking is exposed for shim rendering.
func ValidThinking() map[string]bool { return validThinking }

// ClaudeTiers lists the built-in claude model tiers.
func ClaudeTiers() []string { return claudeTiers }

// ValidReasoning is exposed for shim rendering.
func ValidReasoning() map[string]bool { return validReasoning }

// ImplicitClaudeVariants is exposed for shim rendering.
func ImplicitClaudeVariants() map[string]bool { return implicitClaudeVariants }
