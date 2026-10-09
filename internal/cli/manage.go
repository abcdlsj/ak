package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

// providerFlag maps a command-line flag onto a provider field.
type providerFlag struct {
	name, def, usage string
	set              func(p *config.Provider, v string)
}

var providerFlags = []providerFlag{
	{"kind", "claude", "Engine: claude, codex or pi", func(p *config.Provider, v string) { p.Kind = config.Kind(v) }},
	{"base-url", "", "API endpoint", func(p *config.Provider, v string) { p.BaseURL = v }},
	{"key", "", "API key, or a reference: env:NAME, cmd:..., keychain:...", func(p *config.Provider, v string) { p.SetKey(v) }},
	{"model", "", "Primary model", func(p *config.Provider, v string) { p.Model = v }},
	{"haiku", "", "haiku-tier model (claude)", func(p *config.Provider, v string) { p.Haiku = v }},
	{"sonnet", "", "sonnet-tier model (claude)", func(p *config.Provider, v string) { p.Sonnet = v }},
	{"opus", "", "opus-tier model (claude)", func(p *config.Provider, v string) { p.Opus = v }},
	{"key-field", "", "claude auth variable: auth_token or api_key", func(p *config.Provider, v string) { p.KeyField = v }},
	{"wire-api", "", "codex wire API: responses or chat", func(p *config.Provider, v string) { p.WireAPI = v }},
	{"reasoning", "", "codex default reasoning effort", func(p *config.Provider, v string) { p.Reasoning = v }},
	{"default-variant", "", "Variant applied when launching without one: a thinking/reasoning level or a model tier", func(p *config.Provider, v string) { p.DefaultVariant = v }},
	{"pi-provider", "", "Existing pi provider id to use (skips models.json)", func(p *config.Provider, v string) { p.PiProvider = v }},
	{"pi-api", "", "pi wire API: anthropic-messages, openai-completions, openai-responses", func(p *config.Provider, v string) { p.PiAPI = v }},
	{"pi-auth-header", "", "Send the pi key as Authorization: Bearer (true)", func(p *config.Provider, v string) { p.PiAuthHeader = isTrueFlag(v) }},
	{"config-dir", "", "Isolate claude's whole config here (CLAUDE_CONFIG_DIR)", func(p *config.Provider, v string) { p.ConfigDir = v }},
	{"codex-home", "", "Isolate codex's whole home here (CODEX_HOME)", func(p *config.Provider, v string) { p.CodexHome = v }},
	{"display", "", "Name shown in listings", func(p *config.Provider, v string) { p.Display = v }},
	{"quota", "", "Balance source id (deepseek, kimi, zhipu, newapi, …), or off", func(p *config.Provider, v string) { p.Quota = v }},
	{"quota-cmd", "", "Shell command printing the balance as JSON or a number", func(p *config.Provider, v string) { p.QuotaCmd = v }},
}

func addProviderFlags(cmd *cobra.Command, withDefaults bool) {
	for _, f := range providerFlags {
		def := ""
		if withDefaults {
			def = f.def
		}
		cmd.Flags().String(f.name, def, f.usage)
	}
}

// applyProviderFlags sets fields from flags; onlyChanged skips flags the user
// did not pass, so an edit leaves every other field alone.
func applyProviderFlags(cmd *cobra.Command, p *config.Provider, onlyChanged bool) (changed int) {
	for _, f := range providerFlags {
		if onlyChanged && !cmd.Flags().Changed(f.name) {
			continue
		}
		v, _ := cmd.Flags().GetString(f.name)
		f.set(p, strings.TrimSpace(v))
		if cmd.Flags().Changed(f.name) {
			changed++
		}
	}
	return changed
}

// addPoolFlags adds the flags that turn a provider into a routing pool.
func addPoolFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("member", nil, "pool member provider name (repeatable)")
	cmd.Flags().String("strategy", "", "pool strategy: order, rotate or least-used")
	cmd.Flags().StringArray("map", nil, "pool model mapping member=model (repeatable)")
}

// applyPoolFlags sets the pool fields; onlyChanged skips flags the user did not
// pass, so an edit leaves every other field alone. Passing --member replaces
// the whole member list.
func applyPoolFlags(cmd *cobra.Command, p *config.Provider, onlyChanged bool) (changed int, err error) {
	if cmd.Flags().Changed("member") || !onlyChanged {
		v, _ := cmd.Flags().GetStringArray("member")
		p.Members = parseMemberList(v)
		if cmd.Flags().Changed("member") {
			changed++
		}
	}
	if cmd.Flags().Changed("strategy") || !onlyChanged {
		p.Strategy, _ = cmd.Flags().GetString("strategy")
		if cmd.Flags().Changed("strategy") {
			changed++
		}
	}
	if cmd.Flags().Changed("map") || !onlyChanged {
		v, _ := cmd.Flags().GetStringArray("map")
		m, perr := parseMemberModels(v)
		if perr != nil {
			return changed, perr
		}
		p.MemberModels = m
		if cmd.Flags().Changed("map") {
			changed++
		}
	}
	return changed, nil
}

// applySettingsFlag sets the claude settings layer from --settings, a JSON
// object; an empty value clears it.
func applySettingsFlag(cmd *cobra.Command, p *config.Provider) (int, error) {
	if !cmd.Flags().Changed("settings") {
		return 0, nil
	}
	v, _ := cmd.Flags().GetString("settings")
	if strings.TrimSpace(v) == "" {
		p.Settings = nil
		return 1, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(v), &m); err != nil {
		return 0, fmt.Errorf("--settings must be a JSON object: %w", err)
	}
	p.Settings = m
	return 1, nil
}

// parseMemberList trims and drops empty member names.
func parseMemberList(in []string) []string {
	var out []string
	for _, m := range in {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// parseMemberModels parses repeated member=model flags.
func parseMemberModels(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, s := range in {
		m, model, ok := strings.Cut(s, "=")
		if !ok || strings.TrimSpace(m) == "" || strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("invalid --map %q, expected member=model", s)
		}
		out[strings.TrimSpace(m)] = strings.TrimSpace(model)
	}
	return out, nil
}

// isTrueFlag reports whether a string flag value means true.
func isTrueFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func newAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add [name]",
		Short: "Add a provider",
		Long: `Add a provider and generate its command immediately.

  ak add kimi --kind claude --base-url https://api.moonshot.cn/anthropic --key sk-... --model kimi-k2.7-code
  ak add kimi --preset kimi-coding --key sk-...   # endpoint and model from a preset
  ak add pool --kind claude --member kimi --member cpa --strategy rotate
  ak add        # with no --base-url or --member, opens a form`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			var p config.Provider
			if id, _ := cmd.Flags().GetString("preset"); id != "" {
				if p, err = presetProvider(cmd, id); err != nil {
					return err
				}
			} else {
				applyProviderFlags(cmd, &p, false)
			}
			if _, err := applyPoolFlags(cmd, &p, false); err != nil {
				return err
			}
			if _, err := applySettingsFlag(cmd, &p); err != nil {
				return err
			}
			if p.BaseURL == "" && !p.IsPool() && p.PiProvider == "" {
				d := ui.NewDraft(name)
				if err := runForm(d, cfg); err != nil {
					return err
				}
				name, p = d.Name, d.Provider()
			}
			if name == "" {
				return fmt.Errorf("a provider name is required")
			}
			if err := core.Add(cfg, name, p); err != nil {
				return err
			}
			return runSync(cfg, false)
		},
	}
	addProviderFlags(cmd, true)
	addPoolFlags(cmd)
	cmd.Flags().String("settings", "", `claude settings layer as a JSON object, e.g. '{"effortLevel":"low"}'; '' clears it`)
	cmd.Flags().String("preset", "", "Start from a built-in preset (see `ak preset list`); other flags override it")
	return cmd
}

func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a provider",
		Long: `Change a provider and regenerate its command.

  ak edit kimi --model kimi-k3    # change only the given fields
  ak edit kimi                    # with no flags, opens a form`,
		Args: cobra.ExactArgs(1),
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
			newName := name
			changed := applyProviderFlags(cmd, &p, true)
			n, err := applyPoolFlags(cmd, &p, true)
			if err != nil {
				return err
			}
			changed += n
			n, err = applySettingsFlag(cmd, &p)
			if err != nil {
				return err
			}
			changed += n
			if changed == 0 {
				d := ui.EditDraft(name, p)
				if err := runForm(d, cfg); err != nil {
					return err
				}
				newName, p = d.Name, d.Provider()
			}
			if err := core.Edit(cfg, name, newName, p); err != nil {
				return err
			}
			return runSync(cfg, false)
		},
	}
	addProviderFlags(cmd, false)
	addPoolFlags(cmd)
	cmd.Flags().String("settings", "", `claude settings layer as a JSON object, e.g. '{"effortLevel":"low"}'; '' clears it`)
	return cmd
}

// runForm runs the provider form; a new provider is first asked its engine
// and an optional preset, which pre-fills the form.
func runForm(d *ui.Draft, cfg *config.Config) error {
	if d.IsNew() {
		if err := runHuh(d.PresetForm()); err != nil {
			return err
		}
		d.ApplyPreset()
	}
	return runHuh(d.Form(func(n string) bool { _, ok := cfg.Providers[n]; return ok }))
}

func runHuh(f *huh.Form) error {
	err := f.Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return fmt.Errorf("cancelled")
	}
	return err
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
			if err := core.Remove(cfg, args[0]); err != nil {
				return err
			}
			return runSync(cfg, false)
		},
	}
}

func newRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rename <name> <new-name>",
		Aliases: []string{"mv"},
		Short:   "Rename a provider, and so its command",
		Long: `Rename a provider. Its command becomes ak-<new-name> and the old one is
removed; the default and usage history follow the new name.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := core.Rename(cfg, args[0], args[1]); err != nil {
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
			if err := core.SetDefault(cfg, args[0]); err != nil {
				return err
			}
			fmt.Printf("default provider set to %s\n", args[0])
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
			// Show what a launch would actually use: the provider's default,
			// unless a variant argument overrides it (pass "" for none).
			variant := p.DefaultVariant
			if len(args) > 1 {
				variant = args[1]
			}

			key, err := secrets.Default().Resolve(p)
			if err != nil {
				return err
			}
			eng := provider.EngineFor(p.Kind)
			if eng == nil {
				return fmt.Errorf("provider %q has unsupported kind %q", name, p.Kind)
			}
			launch, err := eng.Launch(name, p, provider.Literal(key), provider.Context{})
			if err != nil {
				return err
			}
			view, err := launchView(launch, variant)
			if err != nil {
				return err
			}
			printEnvPlan(view, cfg.Settings.Prefix+name)
			return nil
		},
	}
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

// newKeyCmd prints a provider's resolved key. Commands for providers using
// api_key_ref call it at launch so no plaintext copy is written to disk.
func newKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__key <name>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			p, ok := cfg.Providers[args[0]]
			if !ok {
				return fmt.Errorf("provider %q does not exist", args[0])
			}
			key, err := secrets.Default().Resolve(p)
			if err != nil {
				return err
			}
			fmt.Print(key)
			return nil
		},
	}
}
