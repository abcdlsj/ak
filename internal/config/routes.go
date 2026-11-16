package config

import "strings"

// A service route and a provider pool were the same idea written twice: one
// spreads a name over members on the listener, the other over members through
// the loopback gateway. A route may now name a provider pool instead of
// repeating its members, so a pool is defined once and answers both as its own
// command and as a model name.
//
// The two shapes are kept in agreement the way a vendor's engines and
// endpoints are: load expands the provider into the accounts and models it
// answers with, save writes only what the file said, and nothing downstream
// has to know which shape it read.

// expandRoutes fills each provider-backed route's members from the provider it
// names, and its strategy when the route leaves that to the provider. The
// members are the accounts and models the provider answers with, in the order
// it would answer, so a route's own strategy applies over that whole order.
func (c *Config) expandRoutes() {
	if len(c.Service.Routes) == 0 {
		return
	}
	routes := make(map[string]Route, len(c.Service.Routes))
	for name, r := range c.Service.Routes {
		if r.Provider != "" {
			if p, ok := c.Providers[r.Provider]; ok {
				r.Members = c.refNames(r.Provider)
				if r.Strategy == "" {
					r.Strategy = p.Strategy
				}
			}
		}
		routes[name] = r
	}
	c.Service.Routes = routes
}

// refNames is the members a route naming provider takes: the accounts and
// models the provider answers with.
func (c *Config) refNames(provider string) []string {
	refs := c.Refs(provider)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Name())
	}
	return out
}

// collapseRoutes is expandRoutes the other way: a route that names a provider
// writes only that, since the members are the provider's to keep.
func (c *Config) collapseRoutes() {
	if len(c.Service.Routes) == 0 {
		return
	}
	routes := make(map[string]Route, len(c.Service.Routes))
	for name, r := range c.Service.Routes {
		if r.Provider != "" {
			r.Members = nil
			if p, ok := c.Providers[r.Provider]; ok && r.Strategy == p.Strategy {
				r.Strategy = ""
			}
		}
		routes[name] = r
	}
	c.Service.Routes = routes
}

// MemberRef reads a pool member: a provider name, an account, or an account
// and the model to ask it for, as <account>/<model>. A provider wins, since a
// provider is what a member usually is.
func (c *Config) MemberRef(member string) (ModelRef, bool) {
	// A provider of its own wins; one ak made for this very member does not
	// count against it.
	if p, ok := c.Providers[member]; ok && !p.implicit {
		return ModelRef{}, false
	}
	if _, ok := c.Accounts[member]; ok {
		return ModelRef{Account: member}, true
	}
	account, model, ok := strings.Cut(member, "/")
	if !ok || model == "" {
		return ModelRef{}, false
	}
	if _, ok := c.Accounts[account]; !ok {
		return ModelRef{}, false
	}
	return ModelRef{Account: account, Model: model}, true
}

// expandMembers makes a provider for every member that names an account, so
// health, quota, checks and the gateway all see one kind of thing: a provider
// on an account, asked for the model the member named.
func (c *Config) expandMembers() {
	for name, p := range c.Providers {
		if p.implicit {
			delete(c.Providers, name)
		}
	}
	for _, name := range c.Names() {
		pool := c.Providers[name]
		if !pool.IsPool() {
			continue
		}
		for _, member := range pool.Members {
			if _, ok := c.Providers[member]; ok {
				continue
			}
			ref, ok := c.MemberRef(member)
			if !ok {
				continue // Validate says what is wrong with it
			}
			p, ok := c.OnAccount(ref.Account, pool.Kind)
			if !ok {
				continue
			}
			if ref.Model != "" {
				p.Model = ref.Model
			}
			p.implicit = true
			c.Providers[member] = p
		}
	}
}
