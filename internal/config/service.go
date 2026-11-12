package config

import (
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"time"
)

// Service is what `ak serve` needs to answer other machines: where it
// listens, the routes model names pick besides accounts, and the tokens
// that may call it. See docs/service.md.
type Service struct {
	// Addr is where the service listens, host:port. Empty: no service, only
	// the loopback pool gateway.
	Addr string `toml:"addr,omitempty"`
	// AdminPassword is the bcrypt hash of the web UI's password.
	AdminPassword string `toml:"admin_password,omitempty"`
	// Routes are the aliases and pools, by the model name that picks them.
	Routes map[string]Route `toml:"routes,omitempty"`
	// Tokens are the bearer tokens that may call the service, by name.
	Tokens map[string]Token `toml:"tokens,omitempty"`
}

// Route is a model name of its own: an alias of another model name, or a
// pool spreading requests over several.
type Route struct {
	// Model makes the route an alias of that model name.
	Model string `toml:"model,omitempty"`
	// Members make the route a pool over those model names, in order.
	Members []string `toml:"members,omitempty"`
	// Strategy is how a pool picks a member, as a provider pool's does.
	Strategy string `toml:"strategy,omitempty"`
}

// IsPool reports whether the route spreads over members.
func (r Route) IsPool() bool { return len(r.Members) > 0 }

// Token is one machine's access. Only the hash of the token is kept.
type Token struct {
	// Hash is "sha256:" and the hex digest of the token.
	Hash string `toml:"hash"`
	// Routes limit the token to these routes and accounts; empty allows all.
	Routes  []string  `toml:"routes,omitempty"`
	Created time.Time `toml:"created"`
}

// RouteNames returns the sorted route names.
func (s Service) RouteNames() []string { return slices.Sorted(maps.Keys(s.Routes)) }

// TokenNames returns the sorted token names.
func (s Service) TokenNames() []string { return slices.Sorted(maps.Keys(s.Tokens)) }

// ModelRef is what a model name names: a route, or an account and the
// model to ask it for (empty: its vendor's default).
type ModelRef struct {
	Route   string
	Account string
	Model   string
}

// ParseModel resolves a model name: <account>/<model>, <account> alone, or
// a route. The split is at the first slash, so a model may hold slashes of
// its own.
func (c *Config) ParseModel(name string) (ModelRef, error) {
	if acct, model, ok := strings.Cut(name, "/"); ok {
		if _, found := c.Accounts[acct]; !found {
			return ModelRef{}, fmt.Errorf("no account %q", acct)
		}
		if model == "" {
			return ModelRef{}, fmt.Errorf("model name %q has nothing after the slash", name)
		}
		return ModelRef{Account: acct, Model: model}, nil
	}
	if _, ok := c.Service.Routes[name]; ok {
		return ModelRef{Route: name}, nil
	}
	if _, ok := c.Accounts[name]; ok {
		return ModelRef{Account: name}, nil
	}
	return ModelRef{}, fmt.Errorf("no account or route %q", name)
}

// OnAccount is a provider on the account for engine kind, as the account
// alone gives it: the vendor's endpoint and the account's key.
func (c *Config) OnAccount(account string, kind Kind) (Provider, bool) {
	v, ok := c.Vendor(Provider{Account: account})
	if !ok {
		return Provider{}, false
	}
	if _, ok := v.Endpoint(string(kind)); !ok {
		return Provider{}, false
	}
	return c.resolve(Provider{Kind: kind, Account: account}), true
}

// validateService checks the [service] section.
func validateService(cfg *Config) error {
	s := cfg.Service
	if s.Addr != "" {
		if _, _, err := net.SplitHostPort(s.Addr); err != nil {
			return fmt.Errorf("service.addr %q: %w", s.Addr, err)
		}
	}
	for _, name := range s.RouteNames() {
		if err := validateRoute(cfg, name); err != nil {
			return err
		}
	}
	for _, name := range s.TokenNames() {
		if err := validateToken(cfg, name, s.Tokens[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateRoute(cfg *Config, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid route name %q: only lowercase letters, digits, dots, underscores and hyphens are allowed", name)
	}
	if _, ok := cfg.Accounts[name]; ok {
		return fmt.Errorf("route %q has an account's name; the model name %q would be ambiguous", name, name)
	}
	r := cfg.Service.Routes[name]
	switch {
	case r.Model != "" && r.IsPool():
		return fmt.Errorf("route %q sets both model and members; an alias names one model, a pool several", name)
	case r.Model == "" && !r.IsPool():
		return fmt.Errorf("route %q needs model (an alias) or members (a pool)", name)
	case r.Model != "" && r.Strategy != "":
		return fmt.Errorf("route %q is an alias; strategy is for pools", name)
	}
	switch r.Strategy {
	case "", StrategyOrder, StrategyRotate, StrategyLeastUsed, StrategySmart:
	default:
		return fmt.Errorf("route %q has invalid strategy %q; must be order, rotate, least-used or smart", name, r.Strategy)
	}
	seen := map[string]bool{}
	for _, m := range append(r.Members, r.Model) {
		if m == "" {
			continue
		}
		if seen[m] {
			return fmt.Errorf("route %q lists %q twice", name, m)
		}
		seen[m] = true
		if _, err := cfg.ParseModel(m); err != nil {
			return fmt.Errorf("route %q names %q: %w", name, m, err)
		}
	}
	return routeDepth(cfg, name, nil)
}

// routeDepth rejects a route that leads back to itself or nests deeper than
// MaxPoolDepth.
func routeDepth(cfg *Config, name string, path []string) error {
	if slices.Contains(path, name) {
		return fmt.Errorf("route %q leads back to itself: %s", path[0], strings.Join(append(path, name), " → "))
	}
	if len(path) > MaxPoolDepth {
		return fmt.Errorf("route %q nests deeper than %d levels", path[0], MaxPoolDepth)
	}
	r := cfg.Service.Routes[name]
	for _, m := range append(r.Members, r.Model) {
		if _, ok := cfg.Service.Routes[m]; ok {
			if err := routeDepth(cfg, m, append(path, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateToken(cfg *Config, name string, t Token) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid token name %q: only lowercase letters, digits, dots, underscores and hyphens are allowed", name)
	}
	if !strings.HasPrefix(t.Hash, "sha256:") || len(t.Hash) != len("sha256:")+64 {
		return fmt.Errorf("token %q has no sha256 hash; issue it again with `ak serve token add`", name)
	}
	for _, r := range t.Routes {
		_, isRoute := cfg.Service.Routes[r]
		_, isAccount := cfg.Accounts[r]
		if !isRoute && !isAccount {
			return fmt.Errorf("token %q is limited to %q, which is no route or account", name, r)
		}
	}
	return nil
}
