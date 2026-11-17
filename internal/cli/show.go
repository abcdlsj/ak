package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/spf13/cobra"
)

func newShowCmd() *cobra.Command {
	var (
		asJSON  bool
		refresh bool
	)
	cmd := &cobra.Command{
		Use:               "show <name>",
		ValidArgsFunction: completeShow,
		Short:             "Inspect a provider, pool, route or account and its resolved legs",
		Long: `Show everything resolved for a provider, pool, service route or account.

For a provider or pool, prints its effective configuration and the ordered
upstream legs (member, account, protocol, model, endpoint). For a service route,
prints the accounts and models it fans out to and how each protocol reaches
them. For an account, prints its vendor, credentials and providers.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			routes := core.RemoteRoutes(cfg)
			if refresh || routes.Stale() {
				if fresh, err := core.FetchRemoteRoutes(cmd.Context(), cfg); err == nil {
					routes = fresh
				} else if refresh {
					warnf("ak: %v; remote models are as last seen", err)
				}
			}

			switch {
			case slices.Contains(cfg.Names(), name):
				rep := buildShowProvider(cfg, name, routes)
				if asJSON {
					return writeShowJSON(rep)
				}
				printShowProvider(cfg, name, rep)
				return nil

			case hasRoute(cfg, name):
				rep, err := buildShowRoute(cfg, name)
				if err != nil {
					return err
				}
				if asJSON {
					return writeShowJSON(rep)
				}
				printShowRoute(cfg, name, rep)
				return nil

			case hasAccount(cfg, name):
				rep := buildShowAccount(cfg, name)
				if asJSON {
					return writeShowJSON(rep)
				}
				printShowAccount(cfg.Accounts[name], rep)
				return nil
			}
			return fmt.Errorf("%q is not a provider, route or account; see `ak list`, `ak accounts` or `ak serve route list`", name)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Ask remote services again what their names answer with")
	return cmd
}

type accountEndpoint struct {
	Protocol string `json:"protocol"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url"`
}

type showReport struct {
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Command    string            `json:"command,omitempty"`
	Engine     string            `json:"engine,omitempty"`
	Display    string            `json:"display,omitempty"`
	Default    bool              `json:"default,omitempty"`
	Account    string            `json:"account,omitempty"`
	Vendor     string            `json:"vendor,omitempty"`
	User       string            `json:"user,omitempty"`
	Model      string            `json:"model,omitempty"`
	Serves     []string          `json:"serves,omitempty"`
	Strategy   string            `json:"strategy,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Target     string            `json:"target,omitempty"`
	ContextWin int               `json:"context_window,omitempty"`
	MaxTokens  int               `json:"max_tokens,omitempty"`
	Variants   []string          `json:"variants,omitempty"`
	DefVariant string            `json:"default_variant,omitempty"`
	Health     string            `json:"health,omitempty"`
	OK         bool              `json:"ok,omitempty"`
	Problems   []string          `json:"problems,omitempty"`
	Users      []string          `json:"users,omitempty"`
	SignIn     string            `json:"sign_in,omitempty"`
	Legs       []core.Leg        `json:"legs,omitempty"`
	RouteLegs  []core.RouteLeg   `json:"route_legs,omitempty"`
	Endpoints  []accountEndpoint `json:"endpoints,omitempty"`
}

func hasRoute(cfg *config.Config, name string) bool {
	_, ok := cfg.Service.Routes[name]
	return ok
}

func hasAccount(cfg *config.Config, name string) bool {
	_, ok := cfg.Accounts[name]
	return ok
}

func writeShowJSON(rep showReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func buildShowProvider(cfg *config.Config, name string, routes core.RemoteModels) showReport {
	p := cfg.Providers[name]
	st := core.QuickStatus(cfg, name)
	var variants []string
	for k := range p.Variants {
		variants = append(variants, k)
	}
	sort.Strings(variants)
	kind := "provider"
	if p.IsPool() {
		kind = "pool"
	}
	return showReport{
		Kind:       kind,
		Name:       name,
		Command:    commandOf(cfg, name),
		Engine:     string(p.Kind),
		Display:    p.Display,
		Default:    cfg.Settings.Default == name,
		Account:    p.Account,
		Model:      p.Model,
		Serves:     core.ModelNames(cfg, name, routes.Models),
		Strategy:   poolStrategy(p),
		ContextWin: p.ContextWindow,
		MaxTokens:  p.MaxTokens,
		Variants:   variants,
		DefVariant: p.DefaultVariant,
		OK:         st.OK(),
		Problems:   st.Problems(),
		Legs:       core.ProviderLegs(cfg, name, routes.Models),
	}
}

func poolStrategy(p config.Provider) string {
	if !p.IsPool() {
		return ""
	}
	return p.StrategyOrDefault()
}

func printShowProvider(cfg *config.Config, name string, rep showReport) {
	p := cfg.Providers[name]
	head := out.Bold(name) + "  " + out.Dim(fmt.Sprintf("(%s · %s)", rep.Command, rep.Engine))
	if rep.Default {
		head += "  " + out.Warn("★ default")
	}
	fmt.Println(head)

	kv := func(label, val string) {
		if val != "" {
			fmt.Printf("  %-11s %s\n", out.Dim(label), val)
		}
	}
	if p.Display != "" && p.Display != name {
		kv("display", p.Display)
	}
	if p.Account != "" {
		accLabel := p.Account
		if v, ok := cfg.Vendor(p); ok {
			accLabel += " · " + v.Name
		}
		kv("account", accLabel)
	}
	if !p.IsPool() {
		kv("sign-in", showSignIn(cfg, p))
	}
	if p.Model != "" || len(rep.Serves) > 0 {
		kv("model", out.Models(p.Model, rep.Serves))
	}
	if p.Kind == config.KindClaude && (p.Opus != p.Model || p.Sonnet != p.Model || p.Haiku != p.Model) {
		kv("tiers", fmt.Sprintf("opus %s · sonnet %s · haiku %s", dash(p.Opus), dash(p.Sonnet), dash(p.Haiku)))
	}
	if p.ContextWindow > 0 || p.MaxTokens > 0 {
		var parts []string
		if p.ContextWindow > 0 {
			parts = append(parts, fmt.Sprintf("context %d", p.ContextWindow))
		}
		if p.MaxTokens > 0 {
			parts = append(parts, fmt.Sprintf("max_tokens %d", p.MaxTokens))
		}
		kv("limits", strings.Join(parts, " · "))
	}
	if p.DefaultVariant != "" || len(rep.Variants) > 0 {
		var parts []string
		if p.DefaultVariant != "" {
			parts = append(parts, "default "+p.DefaultVariant)
		}
		if len(rep.Variants) > 0 {
			parts = append(parts, strings.Join(rep.Variants, ", "))
		}
		kv("variants", strings.Join(parts, " · "))
	}
	if h := p.HomeDir(); h != "" {
		kv("directory", shortPath(config.ExpandHome(h)))
	}
	if p.IsPool() {
		kv("strategy", rep.Strategy)
	}
	kv("health", healthText(core.QuickStatus(cfg, name)))

	if len(rep.Legs) == 0 {
		return
	}
	fmt.Println()
	var tableHead []string
	var rows [][]string
	if p.IsPool() {
		tableHead = []string{"member", "account", "protocol", "model", "endpoint"}
		for _, lg := range rep.Legs {
			ep := lg.Endpoint
			if ep != "" && (lg.Protocol == "native" || lg.Protocol == "antigravity") {
				ep = shortPath(config.ExpandHome(ep))
			}
			rows = append(rows, []string{
				lg.Member,
				out.Or(lg.Account),
				out.Or(lg.Protocol),
				out.Models(lg.Model, lg.Serves),
				out.Or(ep),
			})
		}
	} else {
		tableHead = []string{"account", "protocol", "model", "endpoint"}
		for _, lg := range rep.Legs {
			ep := lg.Endpoint
			if ep != "" && (lg.Protocol == "native" || lg.Protocol == "antigravity") {
				ep = shortPath(config.ExpandHome(ep))
			}
			rows = append(rows, []string{
				out.Or(lg.Account),
				out.Or(lg.Protocol),
				out.Models(lg.Model, lg.Serves),
				out.Or(ep),
			})
		}
	}
	out.Table(os.Stdout, tableHead, rows)
}

func showSignIn(cfg *config.Config, p config.Provider) string {
	if p.UsesEngineLogin() {
		auth := provider.ReadNativeAuth(p)
		switch auth.State {
		case "ok":
			if auth.Account != "" {
				return out.Mark(out.Fine) + " " + p.Kind.CommandName() + " " + auth.Account
			}
			return out.Mark(out.Fine) + " signed in"
		case "expiring", "expired":
			if auth.Account != "" {
				return out.Mark(out.Notice) + " " + auth.State + " (" + auth.Account + ")"
			}
			return out.Mark(out.Notice) + " " + auth.State
		case "missing":
			return out.Mark(out.Broken) + " not signed in"
		default:
			return out.Mark(out.Unknown) + " engine sign-in"
		}
	}
	if p.Account != "" {
		return accountKey(cfg.Accounts[p.Account])
	}
	if p.APIKeyRef != "" {
		return out.Mark(out.Fine) + " ref " + p.APIKeyRef
	}
	if p.APIKey != "" {
		return out.Mark(out.Fine) + " set (" + maskKeyShort(p.APIKey) + ")"
	}
	return out.Mark(out.Broken) + " missing"
}

func maskKeyShort(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + "…" + k[len(k)-4:]
}

func buildShowRoute(cfg *config.Config, name string) (showReport, error) {
	r := cfg.Service.Routes[name]
	legs, err := core.RouteLegs(cfg, name)
	if err != nil {
		return showReport{}, err
	}
	kind := "alias"
	strategy := ""
	if r.IsPool() {
		kind = "pool"
		strategy = cmp.Or(r.Strategy, config.StrategyOrder)
	}
	return showReport{
		Kind:      "route (" + kind + ")",
		Name:      name,
		Target:    r.Model,
		Provider:  r.Provider,
		Strategy:  strategy,
		RouteLegs: legs,
	}, nil
}

func printShowRoute(_ *config.Config, name string, rep showReport) {
	fmt.Println(out.Bold(name) + "  " + out.Dim("("+rep.Kind+")"))
	kv := func(label, val string) {
		if val != "" {
			fmt.Printf("  %-11s %s\n", out.Dim(label), val)
		}
	}
	kv("alias of", rep.Target)
	kv("provider", rep.Provider)
	kv("strategy", rep.Strategy)
	if len(rep.RouteLegs) == 0 {
		return
	}
	fmt.Println()
	rows := make([][]string, 0, len(rep.RouteLegs))
	for _, lg := range rep.RouteLegs {
		rows = append(rows, []string{
			lg.Member,
			lg.Account,
			out.Or(lg.Model),
			reachCell(lg.Protocols[vendor.ProtocolAnthropic]),
			reachCell(lg.Protocols[vendor.ProtocolResponses]),
			reachCell(lg.Protocols[vendor.ProtocolChat]),
			out.Or(lg.Endpoint),
		})
	}
	out.Table(os.Stdout, []string{"member", "account", "model", "claude", "codex", "chat", "endpoint"}, rows)
}

func reachCell(state string) string {
	switch state {
	case "native":
		return out.OK("native")
	case "translated":
		return out.Dim("translated")
	default:
		return out.Or("")
	}
}

func buildShowAccount(cfg *config.Config, name string) showReport {
	a := cfg.Accounts[name]
	v, _ := vendor.Lookup(a.Vendor)
	var eps []accountEndpoint
	for _, proto := range v.EndpointProtocols() {
		if e, ok := v.EndpointFor(proto); ok {
			eps = append(eps, accountEndpoint{Protocol: proto, Model: e.Model, BaseURL: e.BaseURL})
		}
	}
	signIn := "missing"
	switch {
	case a.APIKeyRef != "":
		signIn = "ref " + a.APIKeyRef
	case a.APIKey != "":
		signIn = "set"
	}
	return showReport{
		Kind:      "account",
		Name:      name,
		Vendor:    cmp.Or(v.Name, a.Vendor),
		User:      a.User,
		Model:     a.Model,
		SignIn:    signIn,
		Users:     cfg.UsersOf(name),
		Endpoints: eps,
	}
}

func printShowAccount(a config.Account, rep showReport) {
	fmt.Println(out.Bold(rep.Name) + "  " + out.Dim("(account · "+rep.Vendor+")"))
	kv := func(label, val string) {
		if val != "" {
			fmt.Printf("  %-11s %s\n", out.Dim(label), val)
		}
	}
	kv("user", rep.User)
	kv("sign-in", accountKey(a))
	kv("model", rep.Model)
	if len(rep.Users) > 0 {
		kv("providers", strings.Join(rep.Users, ", "))
	}
	if len(rep.Endpoints) == 0 {
		return
	}
	fmt.Println()
	rows := make([][]string, 0, len(rep.Endpoints))
	for _, ep := range rep.Endpoints {
		rows = append(rows, []string{ep.Protocol, out.Or(ep.Model), ep.BaseURL})
	}
	out.Table(os.Stdout, []string{"protocol", "default model", "endpoint"}, rows)
}
