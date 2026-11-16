package config

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
