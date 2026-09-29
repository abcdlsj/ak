package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
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
  ak import --from codexa            # ~/.codex/profiles/*/
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
			case "codexa":
				return importCodexa(cfg)
			case "cc-switch":
				return importCCSwitch(cfg, all)
			case "pi":
				return importPi(cfg)
			default:
				return fmt.Errorf("--from must be claude-settings, codexa, pi or cc-switch, got %q", from)
			}
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Source: claude-settings, codexa, pi or cc-switch")
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
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		"API_TIMEOUT_MS",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS",
	} {
		if v := env[k]; v != "" {
			p.Env[k] = v
		}
	}
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
	if err := runSync(cfg, false); err != nil {
		return err
	}

	fmt.Printf("\nimported as provider %q.\n\n", name)
	fmt.Println("Now remove these keys from settings.json by hand, otherwise they")
	fmt.Println("override the environment the ak-* commands inject:")
	for _, k := range providerEnvKeys {
		if _, ok := env[k]; ok {
			fmt.Printf("    %s\n", k)
		}
	}
	fmt.Printf("\n  file: %s\n", path)
	fmt.Println("  Keep the global preference keys there (NODE_EXTRA_CA_CERTS,")
	fmt.Println("  CLAUDE_AUTOCOMPACT_PCT_OVERRIDE and friends).")
	fmt.Printf("\nThen verify with `ak-%s -p 'reply OK'`.\n", name)
	return nil
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

// importCodexa reads codexa's profiles and merges duplicates.
//
// The source is only ever read: profile directories hold GB-sized log
// databases, some symlinked back to the primary home, and deleting or
// following those is irreversible.
func importCodexa(cfg *config.Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".codex", "profiles")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return fmt.Errorf("%s does not exist, codexa may never have been used", root)
	}
	if err != nil {
		return err
	}

	type imported struct {
		name string
		p    config.Provider
	}
	var got []imported
	seen := map[string]string{} // (base_url, model, wire) -> provider name already used

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		profile := e.Name()
		cfgPath := filepath.Join(root, profile, "config.toml")
		raw, err := os.ReadFile(cfgPath)
		if err != nil || len(raw) == 0 {
			fmt.Printf("  skip %s: no config.toml or it is empty\n", profile)
			continue
		}

		prof, err := parseCodexaProfile(raw)
		if err != nil {
			fmt.Printf("  skip %s: %v\n", profile, err)
			continue
		}
		if prof.BaseURL == "" {
			fmt.Printf("  skip %s: no base_url found\n", profile)
			continue
		}

		// Deduplicate: profiles sharing base_url, model and wire_api are the
		// same provider, and the first (shortest) name wins.
		key := prof.BaseURL + "\x00" + prof.Model + "\x00" + prof.WireAPI
		if prev, ok := seen[key]; ok {
			fmt.Printf("  %s has the same config as %s, merged\n", profile, prev)
			continue
		}

		name := uniqueName(cfg, profile)
		p := config.Provider{
			Kind:       config.KindCodex,
			BaseURL:    prof.BaseURL,
			Model:      prof.Model,
			Reasoning:  prof.Reasoning,
			WireAPI:    prof.WireAPI,
			ProviderID: prof.ProviderID,
			APIKey:     codexaKey(filepath.Join(root, profile), prof.EnvKey),
			Display:    profile,
		}
		if p.APIKey == "" {
			fmt.Printf("  note: no key read for %s, fill it in later with `ak edit %s`\n", profile, name)
		}
		got = append(got, imported{name: name, p: p})
		seen[key] = name
	}

	if len(got) == 0 {
		return fmt.Errorf("no profiles to import")
	}

	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	for _, g := range got {
		cfg.Providers[g.name] = g.p
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if err := runSync(cfg, false); err != nil {
		return err
	}

	fmt.Printf("\nimported %d provider(s). ~/.codex/profiles/ and the codex-* commands are untouched.\n", len(got))
	fmt.Println("Once `ak-<name>` behaves as expected, `ak prune-codexa` removes the legacy commands (dry-run by default).")
	return nil
}

// codexaProfile holds the settings extracted from a codexa profile.
type codexaProfile struct {
	ProviderID string
	BaseURL    string
	WireAPI    string
	Model      string
	Reasoning  string
	EnvKey     string
}

// uniqueName returns a name that does not collide with an existing provider.
func uniqueName(cfg *config.Config, base string) string {
	if _, ok := cfg.Providers[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s%d", base, i)
		if _, ok := cfg.Providers[cand]; !ok {
			return cand
		}
	}
}
