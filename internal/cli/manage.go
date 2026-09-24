package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a provider",
		Long: `Add a provider and generate its command immediately.

Two ways to use it:
  ak add kimi --kind claude --base-url https://api.moonshot.cn/anthropic --key sk-... --model kimi-k2.7-code
  ak add kimi        # with no flags, prompts for each field`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]

			p, err := buildProviderFromFlags(cmd)
			if err != nil {
				return err
			}
			// No substantive input given, so fall back to interactive entry.
			if p.BaseURL == "" {
				p, err = interactiveAdd(name)
				if err != nil {
					return err
				}
			}
			return doAdd(cfg, name, p)
		},
	}

	f := cmd.Flags()
	f.String("kind", "claude", "Engine: claude or codex")
	f.String("base-url", "", "API endpoint")
	f.String("key", "", "API key")
	f.String("model", "", "Primary model")
	f.String("haiku", "", "haiku-tier model (claude)")
	f.String("sonnet", "", "sonnet-tier model (claude)")
	f.String("opus", "", "opus-tier model (claude)")
	f.String("key-field", "auth_token", "claude auth variable: auth_token or api_key")
	f.String("wire-api", "responses", "codex wire API: responses or chat")
	f.String("reasoning", "", "codex default reasoning effort")
	f.String("display", "", "Name shown in listings")
	return cmd
}

func buildProviderFromFlags(cmd *cobra.Command) (config.Provider, error) {
	f := cmd.Flags()
	get := func(n string) string {
		v, _ := f.GetString(n)
		return strings.TrimSpace(v)
	}
	p := config.Provider{
		Kind:      config.Kind(get("kind")),
		BaseURL:   get("base-url"),
		Model:     get("model"),
		Display:   get("display"),
		APIKey:    get("key"),
		KeyField:  get("key-field"),
		WireAPI:   get("wire-api"),
		Reasoning: get("reasoning"),
		Haiku:     get("haiku"),
		Sonnet:    get("sonnet"),
		Opus:      get("opus"),
	}
	return p, nil
}

// doAdd writes the config, validates it, then syncs immediately.
func doAdd(cfg *config.Config, name string, p config.Provider) error {
	if err := config.ValidateName(name); err != nil {
		return err
	}
	if _, exists := cfg.Providers[name]; exists {
		return fmt.Errorf("provider %q already exists, run `ak rm %s` first", name, name)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	cfg.Providers[name] = p

	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	return runSync(cfg, false)
}

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a provider and its generated commands",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Providers[name]; !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			delete(cfg.Providers, name)
			if cfg.Settings.Default == name {
				cfg.Settings.Default = ""
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			return runSync(cfg, false)
		},
	}
}

func newDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default [name]",
		Short: "Show or set the default provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if cfg.Settings.Default == "" {
					fmt.Println("No default provider set.")
					return nil
				}
				fmt.Printf("default provider: %s\n", cfg.Settings.Default)
				return nil
			}
			name := args[0]
			if _, ok := cfg.Providers[name]; !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			cfg.Settings.Default = name
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("default provider set to %s\n", name)
			return nil
		},
	}
}

// newEnvCmd prints the environment a provider would inject, for debugging.
// This is the cheapest way to verify shim behaviour: the exact environment is
// visible without issuing a real request.
func newEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "env <name> [variant]",
		Short: "Print the environment a provider would inject",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			p, ok := cfg.Providers[name]
			if !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			variant := ""
			if len(args) > 1 {
				variant = args[1]
			}

			key, err := secrets.Default().Resolve(p)
			if err != nil {
				return err
			}

			if p.Kind == config.KindClaude {
				printEnvPlan(envPlanViewFrom(claudePlanFor(name, p, variant, key)), cfg.Settings.Prefix+name)
				return nil
			}
			printEnvPlan(envPlanViewFrom(codexPlanFor(name, p, key)), cfg.Settings.Prefix+name)
			fmt.Printf("  (a codex provider also generates ~/.codex/ak-%s.config.toml, applied via --profile)\n", name)
			return nil
		},
	}
}

// interactiveAdd is a minimal stdin prompt. gum is preferred when available.
func interactiveAdd(name string) (config.Provider, error) {
	r := bufio.NewReader(os.Stdin)
	ask := func(label, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", label, def)
		} else {
			fmt.Printf("%s: ", label)
		}
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	kind := ask("engine (claude/codex)", "claude")
	p := config.Provider{
		Kind:     config.Kind(kind),
		BaseURL:  ask("API endpoint", ""),
		Model:    ask("primary model", ""),
		APIKey:   ask("API key", ""),
		Display:  ask("display name", name),
		KeyField: ask("auth variable (auth_token/api_key)", "auth_token"),
		WireAPI:  ask("codex wire API (responses/chat)", "responses"),
	}
	return p, nil
}

func printEnvPlan(plan envPlanView, cmd string) {
	fmt.Printf("# %s would inject:\n", cmd)
	for _, line := range plan {
		fmt.Printf("  %s\n", line)
	}
}

// envPlanView is the masked environment display.
type envPlanView []string

func newUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ui",
		Short: "Open the TUI (same as running ak with no arguments)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ui.RunUI()
		},
	}
}
