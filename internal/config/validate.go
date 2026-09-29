package config

import (
	"fmt"
	"regexp"
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
	"quota": true,
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
	switch p.Kind {
	case KindClaude, KindCodex:
	case "":
		return fmt.Errorf("provider %q is missing kind (claude or codex)", name)
	default:
		return fmt.Errorf("provider %q has invalid kind %q; must be claude or codex", name, p.Kind)
	}
	if p.IsPool() {
		return validatePool(cfg, name, p)
	}
	if p.BaseURL == "" {
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
	if p.Kind == KindCodex {
		switch p.WireAPI {
		case "", "responses", "chat":
		default:
			return fmt.Errorf("provider %q has invalid wire_api %q; must be responses or chat", name, p.WireAPI)
		}
		if r := p.Reasoning; r != "" && !validReasoning[r] {
			return fmt.Errorf("provider %q has invalid reasoning %q", name, r)
		}
	}
	if err := validateQuota(name, p.Quota); err != nil {
		return err
	}
	return validateVariants(name, p)
}

// quotaRe constrains a built-in quota source id; unknown ids are caught with a
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

// validatePool checks a routing pool: its members exist, share its kind, are
// not itself and are not other pools (one level only for now), and its
// strategy and model mapping name members.
func validatePool(cfg *Config, name string, p Provider) error {
	switch p.Strategy {
	case "", StrategyOrder, StrategyRotate, StrategyLeastUsed:
	default:
		return fmt.Errorf("provider %q has invalid strategy %q; must be order, rotate or least-used", name, p.Strategy)
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
		if mp.IsPool() {
			return fmt.Errorf("pool %q names %q, which is itself a pool; pools may not nest", name, m)
		}
		if mp.Kind != p.Kind {
			return fmt.Errorf("pool %q is %s but member %q is %s; a pool's members must share its kind", name, p.Kind, m, mp.Kind)
		}
	}
	for m := range p.MemberModels {
		if !seen[m] {
			return fmt.Errorf("pool %q maps a model for %q, which is not one of its members", name, m)
		}
	}
	return validateVariants(name, p)
}

func validateVariants(name string, p Provider) error {
	banned := claudeSubcommands
	if p.Kind == KindCodex {
		banned = codexSubcommands
	}
	for v := range p.Variants {
		if !nameRe.MatchString(v) {
			return fmt.Errorf("provider %q has invalid variant name %q", name, v)
		}
		if banned[v] {
			return fmt.Errorf("provider %q variant %q collides with a %s subcommand, making that subcommand unreachable", name, v, p.Kind)
		}
		if p.Kind == KindClaude && implicitClaudeVariants[v] {
			return fmt.Errorf("provider %q variant %q collides with a built-in model tier variant", name, v)
		}
		if p.Kind == KindCodex && validReasoning[v] {
			return fmt.Errorf("provider %q variant %q collides with a reasoning effort level", name, v)
		}
	}
	return nil
}

// validReasoning lists codex's reasoning effort levels. They double as the
// implicit variants of a codex shim.
var validReasoning = map[string]bool{
	"minimal": true, "low": true, "medium": true, "high": true,
	"xhigh": true, "max": true,
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

// ClaudeTiers lists the built-in claude model tiers.
func ClaudeTiers() []string { return claudeTiers }

// ValidReasoning is exposed for shim rendering.
func ValidReasoning() map[string]bool { return validReasoning }

// ImplicitClaudeVariants is exposed for shim rendering.
func ImplicitClaudeVariants() map[string]bool { return implicitClaudeVariants }
