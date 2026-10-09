package cli

import (
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/preset"
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
			out = append(out, n)
		}
	}
	return out
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
