package config

import (
	"fmt"
	"regexp"
)

// nameRe 限制供应商名可安全用作命令名后缀。
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Reserved 是 ak 的子命令名。供应商不得占用,否则 `ak <name>` 产生歧义。
var Reserved = map[string]bool{
	"ui": true, "pick": true, "list": true, "ls": true, "add": true,
	"rm": true, "remove": true, "edit": true, "sync": true, "doctor": true,
	"import": true, "default": true, "env": true, "usage": true,
	"completion": true, "help": true, "version": true, "run": true,
	"prune-codexa": true,
}

// claudeSubcommands 与 codexSubcommands 是变体名的禁用集。
// 变体走第一位置参数,若与引擎自身的子命令同名,shim 会把它吞掉导致无法调用该子命令。
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

// ValidateName 校验供应商名。
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("供应商名 %q 非法:只允许小写字母、数字、点、下划线、连字符,且以字母或数字开头", name)
	}
	if Reserved[name] {
		return fmt.Errorf("供应商名 %q 与 ak 子命令冲突", name)
	}
	return nil
}

// Validate 全量校验配置。
func Validate(cfg *Config) error {
	if cfg.Settings.Prefix == "" {
		return fmt.Errorf("settings.prefix 不能为空")
	}
	for _, name := range cfg.Names() {
		if err := ValidateName(name); err != nil {
			return err
		}
		p := cfg.Providers[name]
		if err := validateProvider(name, p); err != nil {
			return err
		}
	}
	if d := cfg.Settings.Default; d != "" {
		if _, ok := cfg.Providers[d]; !ok {
			return fmt.Errorf("settings.default 指向不存在的供应商 %q", d)
		}
	}
	return nil
}

func validateProvider(name string, p Provider) error {
	switch p.Kind {
	case KindClaude, KindCodex:
	case "":
		return fmt.Errorf("供应商 %q 缺少 kind(claude 或 codex)", name)
	default:
		return fmt.Errorf("供应商 %q 的 kind %q 无效,只能是 claude 或 codex", name, p.Kind)
	}
	if p.BaseURL == "" {
		return fmt.Errorf("供应商 %q 缺少 base_url", name)
	}
	if p.APIKey != "" && p.APIKeyRef != "" {
		return fmt.Errorf("供应商 %q 的 api_key 与 api_key_ref 互斥,只能设一个", name)
	}
	if p.Kind == KindClaude {
		switch p.KeyField {
		case "", "auth_token", "api_key":
		default:
			return fmt.Errorf("供应商 %q 的 key_field %q 无效,只能是 auth_token 或 api_key", name, p.KeyField)
		}
	}
	if p.Kind == KindCodex {
		switch p.WireAPI {
		case "", "responses", "chat":
		default:
			return fmt.Errorf("供应商 %q 的 wire_api %q 无效,只能是 responses 或 chat", name, p.WireAPI)
		}
		if r := p.Reasoning; r != "" && !validReasoning[r] {
			return fmt.Errorf("供应商 %q 的 reasoning %q 无效", name, r)
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
			return fmt.Errorf("供应商 %q 的变体名 %q 非法", name, v)
		}
		if banned[v] {
			return fmt.Errorf("供应商 %q 的变体名 %q 与 %s 子命令冲突,会导致该子命令无法调用", name, v, p.Kind)
		}
		if p.Kind == KindClaude && implicitClaudeVariants[v] {
			return fmt.Errorf("供应商 %q 的变体名 %q 与内置档位变体冲突", name, v)
		}
		if p.Kind == KindCodex && validReasoning[v] {
			return fmt.Errorf("供应商 %q 的变体名 %q 与 reasoning effort 档位冲突", name, v)
		}
	}
	return nil
}

// validReasoning 是 codex 的 reasoning effort 档位,也是 codex shim 的隐式变体。
var validReasoning = map[string]bool{
	"minimal": true, "low": true, "medium": true, "high": true,
	"xhigh": true, "max": true,
}

// implicitClaudeVariants 是 claude shim 的内置档位变体。
var implicitClaudeVariants = map[string]bool{
	"opus": true, "sonnet": true, "haiku": true,
}

// ValidReasoning 暴露给 shim 渲染用。
func ValidReasoning() map[string]bool { return validReasoning }

// ImplicitClaudeVariants 暴露给 shim 渲染用。
func ImplicitClaudeVariants() map[string]bool { return implicitClaudeVariants }
