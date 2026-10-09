package cli

import (
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/preset"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/spf13/cobra"
)

// completeProviders completes provider names, each argument once.
func completeProviders(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return providerNames(args, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeFirstProvider completes a provider name for the first argument only.
func completeFirstProvider(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return providerNames(nil, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeTarget completes the first argument of bare ak: cobra adds the
// subcommands, this adds providers, and after a colon the provider's variants.
// Later arguments are the engine's, so files complete as usual.
func completeTarget(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return targetNames(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeFirstTarget completes <provider>[:variant] for the first argument only.
func completeFirstTarget(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return targetNames(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func targetNames(toComplete string) []string {
	cfg, err := core.Load()
	if err != nil {
		return nil
	}
	name, prefix, hasVariant := strings.Cut(toComplete, ":")
	if !hasVariant {
		return matchNames(cfg, nil, toComplete)
	}
	p, ok := cfg.Providers[name]
	if !ok {
		return nil
	}
	eng := provider.EngineFor(p.Kind)
	if eng == nil {
		return nil
	}
	l, err := eng.Launch(name, p, provider.Secret{Deferred: true}, provider.Context{})
	if err != nil {
		return nil
	}
	var out []string
	for _, v := range l.Variants {
		if !v.Quiet && strings.HasPrefix(v.Name, prefix) {
			desc := "variant"
			if v.Name == l.DefaultVariant {
				desc = "default variant"
			}
			out = append(out, name+":"+v.Name+"\t"+desc)
		}
	}
	return out
}

func providerNames(used []string, prefix string) []string {
	cfg, err := core.Load()
	if err != nil {
		return nil
	}
	return matchNames(cfg, used, prefix)
}

func matchNames(cfg *config.Config, used []string, prefix string) []string {
	skip := map[string]bool{}
	for _, u := range used {
		skip[u] = true
	}
	var out []string
	for _, n := range cfg.Names() {
		if !skip[n] && strings.HasPrefix(n, prefix) {
			out = append(out, n+"\t"+describeProvider(cfg, n))
		}
	}
	return out
}

// describeProvider is the one-line summary shown beside a completion, in the
// launcher's order: health, engine, model or pool, default. Health here skips
// the dry-run sync the TUI does, so Tab stays instant: only a missing key or
// engine is marked.
func describeProvider(cfg *config.Config, name string) string {
	p := cfg.Providers[name]
	var parts []string
	if st := core.QuickStatus(cfg, name); !st.OK() {
		parts = append(parts, "✗ "+strings.Join(st.Problems(), ", "))
	}
	parts = append(parts, string(p.Kind))
	if p.IsPool() {
		parts = append(parts, endpoint(listRow{Members: p.Members, Strategy: p.Strategy}))
	} else if p.Model != "" {
		parts = append(parts, p.Model)
	}
	if cfg.Settings.Default == name {
		parts = append(parts, "default")
	}
	return strings.Join(parts, " · ")
}

// completePresets completes preset ids, each once across engines.
func completePresets(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	seen := map[string]bool{}
	var out []string
	for _, p := range preset.All() {
		if !seen[p.ID] && strings.HasPrefix(p.ID, toComplete) {
			seen[p.ID] = true
			out = append(out, p.ID+"\t"+p.Name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
