package cli

import (
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/vendor"
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

// completeVendors completes vendor ids.
func completeVendors(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, v := range vendor.All() {
		if strings.HasPrefix(v.ID, toComplete) {
			out = append(out, v.ID+"\t"+v.Name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeAccounts completes account names.
func completeAccounts(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range cfg.AccountNames() {
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\t"+cfg.Accounts[n].Vendor)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeHolders completes accounts first, then providers not shadowed by an account.
func completeHolders(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	skip := map[string]bool{}
	for _, a := range args {
		skip[a] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range cfg.AccountNames() {
		seen[n] = true
		if !skip[n] && strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\taccount · "+cfg.Accounts[n].Vendor)
		}
	}
	for _, n := range cfg.Names() {
		if !seen[n] && !skip[n] && strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\t"+describeProvider(cfg, n))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeShow completes provider names, service route names, and accounts.
func completeShow(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range cfg.Names() {
		seen[n] = true
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\t"+describeProvider(cfg, n))
		}
	}
	for _, n := range cfg.Service.RouteNames() {
		if !seen[n] && strings.HasPrefix(n, toComplete) {
			seen[n] = true
			out = append(out, n+"\tservice route")
		}
	}
	for _, n := range cfg.AccountNames() {
		if !seen[n] && strings.HasPrefix(n, toComplete) {
			seen[n] = true
			out = append(out, n+"\taccount · "+cfg.Accounts[n].Vendor)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeDefault completes `ak default` arguments: engine or provider first,
// then matching providers or native when an engine was given.
func completeDefault(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 0 {
		var out []string
		for _, k := range config.Kinds {
			n := k.CommandName()
			if strings.HasPrefix(n, toComplete) {
				out = append(out, n+"\tengine default")
			}
		}
		out = append(out, matchNames(cfg, nil, toComplete)...)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 1 {
		k, ok := parseKind(args[0])
		if !ok {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []string
		for _, n := range cfg.Names() {
			if cfg.Providers[n].Kind == k && strings.HasPrefix(n, toComplete) {
				out = append(out, n+"\t"+describeProvider(cfg, n))
			}
		}
		if strings.HasPrefix("native", toComplete) {
			out = append(out, "native\tkeep the engine's own command")
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeRoutes(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range cfg.Service.RouteNames() {
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeRouteTargets(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range cfg.Service.RouteNames() {
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\troute")
		}
	}
	for _, n := range cfg.AccountNames() {
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\taccount · "+cfg.Accounts[n].Vendor)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completePoolProviders(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range cfg.Names() {
		if cfg.Providers[n].IsPool() && strings.HasPrefix(n, toComplete) {
			out = append(out, n+"\t"+describeProvider(cfg, n))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeTokens(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range cfg.Service.TokenNames() {
		if strings.HasPrefix(n, toComplete) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeRemotes(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := core.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, a := range cfg.AccountNames() {
		v, ok := vendor.Lookup(cfg.Accounts[a].Vendor)
		if ok && v.Quota == quota.RemoteSource && strings.HasPrefix(a, toComplete) {
			out = append(out, a+"\t"+v.WebsiteURL)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
