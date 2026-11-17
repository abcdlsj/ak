package core

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/vendor"
)

// Leg is one concrete upstream or engine target a provider resolves to, in
// fallback order.
type Leg struct {
	Member   string   `json:"member"`
	Account  string   `json:"account,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	Model    string   `json:"model,omitempty"`
	Serves   []string `json:"serves,omitempty"`
	Endpoint string   `json:"endpoint,omitempty"`
}

// ProviderLegs resolves the concrete legs of provider name in routing order.
// route, when non-nil, resolves remote service route names to the models
// behind them.
func ProviderLegs(cfg *config.Config, name string, route func(model string) []string) []Leg {
	var out []Leg
	var walk func(name, asked, inherited string, depth int, path map[string]bool)
	walk = func(name, asked, inherited string, depth int, path map[string]bool) {
		if path[name] || depth > config.MaxPoolDepth {
			return
		}
		p, ok := cfg.Providers[name]
		if !ok {
			return
		}
		if !p.IsPool() {
			model := cmp.Or(asked, p.Model, inherited)
			var serves []string
			if route != nil && model != "" {
				if ms := route(model); len(ms) > 0 && !(len(ms) == 1 && ms[0] == model) {
					serves = append([]string(nil), ms...)
				}
			}
			out = append(out, Leg{
				Member:   name,
				Account:  p.Account,
				Protocol: legProtocol(p),
				Model:    model,
				Serves:   serves,
				Endpoint: legEndpoint(p),
			})
			return
		}
		path[name] = true
		for _, member := range p.Members {
			walk(member, p.MemberModels[member], cmp.Or(p.Model, inherited), depth+1, path)
		}
		delete(path, name)
	}
	walk(name, "", "", 0, map[string]bool{})
	return out
}

func legProtocol(p config.Provider) string {
	switch p.Kind {
	case config.KindAntigravity:
		return "antigravity"
	case config.KindClaude:
		if p.UsesEngineLogin() {
			return "native"
		}
		return vendor.ProtocolAnthropic
	case config.KindCodex:
		if p.UsesEngineLogin() {
			return "native"
		}
		if p.WireAPI == "chat" {
			return vendor.ProtocolChat
		}
		return vendor.ProtocolResponses
	case config.KindPi:
		if p.UsesEngineLogin() {
			return "native"
		}
		switch p.PiAPI {
		case "openai-completions":
			return vendor.ProtocolChat
		case "openai-responses":
			return vendor.ProtocolResponses
		}
		return vendor.ProtocolAnthropic
	}
	return ""
}

func legEndpoint(p config.Provider) string {
	if p.UsesEngineLogin() {
		if p.Kind == config.KindPi && p.PiProvider != "" && p.PiDir == "" {
			return "pi:" + p.PiProvider
		}
		return p.HomeDir()
	}
	if p.BaseURL == "" && p.PiProvider != "" {
		return "pi:" + p.PiProvider
	}
	return p.BaseURL
}

// RouteLeg is one account-level target a service route resolves to.
type RouteLeg struct {
	Member    string            `json:"member"`
	Account   string            `json:"account"`
	Model     string            `json:"model,omitempty"`
	Protocols map[string]string `json:"protocols,omitempty"`
	Endpoint  string            `json:"endpoint,omitempty"`
}

// RouteLegs resolves a service route (or account/model target) into its
// ordered upstream legs, following aliases and nested pools.
func RouteLegs(cfg *config.Config, name string) ([]RouteLeg, error) {
	var out []RouteLeg
	var walk func(target string, depth int) error
	walk = func(target string, depth int) error {
		if depth > config.MaxPoolDepth+1 {
			return fmt.Errorf("route %q nests deeper than %d", name, config.MaxPoolDepth)
		}
		ref, err := cfg.ParseModel(target)
		if err != nil {
			return err
		}
		if ref.Account != "" {
			out = append(out, buildRouteLeg(cfg, target, ref))
			return nil
		}
		r := cfg.Service.Routes[ref.Route]
		if r.Model != "" {
			return walk(r.Model, depth+1)
		}
		for _, m := range r.Members {
			if err := walk(m, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(name, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func buildRouteLeg(cfg *config.Config, member string, ref config.ModelRef) RouteLeg {
	protocols := map[string]string{}
	var endpoints []string
	seenEP := map[string]bool{}
	var models []string
	seenModel := map[string]bool{}

	for _, proto := range []string{vendor.ProtocolAnthropic, vendor.ProtocolResponses, vendor.ProtocolChat} {
		if p, ok := cfg.OnAccountProtocol(ref.Account, proto); ok {
			protocols[proto] = "native"
			if p.BaseURL != "" && !seenEP[p.BaseURL] {
				seenEP[p.BaseURL] = true
				endpoints = append(endpoints, p.BaseURL)
			}
			if ref.Model == "" && p.Model != "" && !seenModel[p.Model] {
				seenModel[p.Model] = true
				models = append(models, p.Model)
			}
		}
	}
	hasAny := len(endpoints) > 0 || len(protocols) > 0
	if !hasAny {
		if _, ok := cfg.Vendor(config.Provider{Account: ref.Account}); ok {
			// Check whether the vendor defines any endpoint at all.
			for _, proto := range vendor.Protocols {
				if _, ok := cfg.OnAccountProtocol(ref.Account, proto); ok {
					hasAny = true
					break
				}
			}
		}
	}
	for _, proto := range []string{vendor.ProtocolAnthropic, vendor.ProtocolResponses, vendor.ProtocolChat} {
		if _, ok := protocols[proto]; ok {
			continue
		}
		if hasAny {
			protocols[proto] = "translated"
		} else {
			protocols[proto] = "absent"
		}
	}

	model := ref.Model
	if model == "" {
		model = cmp.Or(cfg.Accounts[ref.Account].Model, strings.Join(models, " / "))
	}

	return RouteLeg{
		Member:    member,
		Account:   ref.Account,
		Model:     model,
		Protocols: protocols,
		Endpoint:  strings.Join(endpoints, ", "),
	}
}
