package core

import (
	"cmp"

	"github.com/abcdlsj/ak/internal/config"
)

// Served is one model a name answers with, and the member it comes from:
// what a client is really served by, once pools, member maps and accounts
// have had their say.
type Served struct {
	Model  string
	Member string
}

// Serves resolves the models a provider answers with. A plain provider
// answers with its own model; a pool with what its members answer with,
// each member's own model unless the pool maps it, and a map deeper down
// winning over one above — the same order the gateway uses when it routes.
//
// A name may be one a remote service chose rather than a model, as a route
// over several models is. route, when not nil, says what models such a name
// may answer with; nil leaves it as the name it is, which is what ak can say
// without asking anyone.
func Serves(cfg *config.Config, name string, route func(model string) []string) []Served {
	seen := map[string]bool{}
	var out []Served
	// asked is what a mapping said to ask this member for; inherited is the
	// nearest name above that says which model it means, which a member that
	// names none of its own falls back to.
	var walk func(name, asked, inherited string, depth int)
	walk = func(name, asked, inherited string, depth int) {
		p, ok := cfg.Providers[name]
		if !ok || depth > config.MaxPoolDepth {
			return
		}
		if !p.IsPool() {
			m := cmp.Or(asked, p.Model, inherited)
			if m == "" || seen[m] {
				return
			}
			seen[m] = true
			out = append(out, Served{Model: m, Member: name})
			return
		}
		for _, member := range p.Members {
			walk(member, p.MemberModels[member], cmp.Or(p.Model, inherited), depth+1)
		}
	}
	walk(name, "", "", 0)

	// A name that is no model is what a route answers with.
	if route == nil {
		return out
	}
	seen = map[string]bool{}
	var resolved []Served
	for _, s := range out {
		models := route(s.Model)
		if len(models) == 0 {
			models = []string{s.Model}
		}
		for _, m := range models {
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			resolved = append(resolved, Served{Model: m, Member: s.Member})
		}
	}
	return resolved
}

// ModelNames is Serves as plain model names, in the order they would answer.
func ModelNames(cfg *config.Config, name string, route func(model string) []string) []string {
	served := Serves(cfg, name, route)
	out := make([]string, 0, len(served))
	for _, s := range served {
		out = append(out, s.Model)
	}
	return out
}
