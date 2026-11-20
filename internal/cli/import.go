package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var from string
	var all bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import providers from an existing configuration",
		Long: `Read providers from an existing configuration into ak.

  ak import --from claude-settings   # env block of ~/.claude/settings.json
  ak import --from pi                # ~/.pi/agent/models.json
  ak import --from cc-switch         # ~/.cc-switch/cc-switch.db, pick from a list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			switch from {
			case "claude-settings":
				return importClaudeSettings(cfg)
			case "cc-switch":
				return importCCSwitch(cfg, all)
			case "pi":
				return importPi(cfg)
			default:
				return fmt.Errorf("--from must be claude-settings, pi, cc-switch or vendor, got %q", from)
			}
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Source: claude-settings, pi or cc-switch")
	cmd.Flags().BoolVar(&all, "all", false, "Import every provider without the selection prompt (cc-switch only)")
	cmd.MarkFlagRequired("from")
	return cmd
}

// importClaudeSettings moves the provider keys out of settings.json into ak.
//
// Only provider-specific keys move. Global preferences (NODE_EXTRA_CA_CERTS,
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC and the like) stay put, because they
// should apply to every provider. Afterwards a list of keys to delete is
// printed; the deletion is left to the user, ak never writes provider keys into
// settings.json.
func importClaudeSettings(cfg *config.Config) error {
	path := claudeSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	env := settings.Env

	if env["ANTHROPIC_BASE_URL"] == "" {
		return fmt.Errorf("%s has no ANTHROPIC_BASE_URL in env, nothing to import", path)
	}

	// "default" is an ak subcommand name, so it cannot name a provider.
	name := "imported"
	if _, exists := cfg.Providers[name]; exists {
		for i := 2; ; i++ {
			cand := fmt.Sprintf("imported%d", i)
			if _, exists := cfg.Providers[cand]; !exists {
				name = cand
				break
			}
		}
	}

	p, _ := claudeProviderFromEnv(env)
	// Carry over the remaining provider-specific keys so they do not linger in
	// settings.json. Only provider keys are collected; global preferences such
	// as NODE_EXTRA_CA_CERTS, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC and
	// CLAUDE_AUTOCOMPACT_PCT_OVERRIDE must stay in settings.json.
	p.Env = map[string]string{}
	for _, k := range []string{
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		"API_TIMEOUT_MS",
	} {
		if v := env[k]; v != "" {
			p.Env[k] = v
		}
	}
	// The two numbers ak hands the engines as fields of their own, so a later
	// `ak sync` keeps telling every engine the same window and reply limit.
	p.ContextWindow = atoiOrZero(env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"])
	p.MaxTokens = atoiOrZero(env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"])
	if len(p.Env) == 0 {
		p.Env = nil
	}

	cfg.Providers[name] = p
	if cfg.Settings.Default == "" {
		cfg.Settings.Default = name
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	out.Done(os.Stdout, "imported as provider %q", name)
	if err := runSync(cfg, false); err != nil {
		return err
	}

	out.Hint(os.Stdout, "Remove these keys from %s by hand, otherwise they override the environment the ak-* commands inject:", shortPath(path))
	for _, k := range providerEnvKeys {
		if _, ok := env[k]; ok {
			fmt.Printf("  %s\n", k)
		}
	}
	out.Hint(os.Stdout, "Keep the global preference keys there (NODE_EXTRA_CA_CERTS, CLAUDE_AUTOCOMPACT_PCT_OVERRIDE and friends).")
	out.Hint(os.Stdout, "Then verify with `ak-%s -p 'reply OK'`.", name)
	return nil
}

// atoiOrZero reads a token count out of an environment block, where it is a
// string that may be blank or, in a hand-edited file, not a number at all.
func atoiOrZero(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// claudeProviderFromEnv maps a claude env block onto a provider. The returned
// set holds the keys that were consumed, so a caller can decide what to do
// with the rest: settings.json keeps global preferences in place, while
// cc-switch treats the whole block as belonging to the provider.
func claudeProviderFromEnv(env map[string]string) (config.Provider, map[string]bool) {
	p := config.Provider{
		Kind:    config.KindClaude,
		BaseURL: env["ANTHROPIC_BASE_URL"],
		Model:   env["ANTHROPIC_MODEL"],
		Haiku:   env["ANTHROPIC_DEFAULT_HAIKU_MODEL"],
		Sonnet:  env["ANTHROPIC_DEFAULT_SONNET_MODEL"],
		Opus:    env["ANTHROPIC_DEFAULT_OPUS_MODEL"],
	}
	if k := env["ANTHROPIC_API_KEY"]; k != "" {
		p.APIKey, p.KeyField = k, "api_key"
	} else if k := env["ANTHROPIC_AUTH_TOKEN"]; k != "" {
		p.APIKey, p.KeyField = k, "auth_token"
	}
	consumed := map[string]bool{
		"ANTHROPIC_BASE_URL":             true,
		"ANTHROPIC_MODEL":                true,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  true,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": true,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   true,
		"ANTHROPIC_API_KEY":              true,
		"ANTHROPIC_AUTH_TOKEN":           true,
	}
	return p, consumed
}

// skipLine names one source entry that was left out, and why.
func skipLine(name, reason string) {
	fmt.Printf("%s %s  %s\n", out.Mark(out.Notice), name, out.Reason(reason))
}
