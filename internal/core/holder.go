package core

import (
	"fmt"

	"github.com/abcdlsj/ak/internal/config"
)

// Holder is what a balance belongs to: an account, which every provider on
// it shares, or a provider with an endpoint and key of its own.
type Holder struct {
	Name    string
	Account bool
	// Provider is what the balance query is asked with: the holder itself,
	// or for an account a provider as the account alone gives it.
	Provider config.Provider
	// Users are, for an account, the providers on it.
	Users []string
}

// Holders lists what holds a balance among names (every provider and
// account when empty), each once, accounts first: a provider on an account
// is its account, a pool its members' holders. A name may be an account's.
func Holders(cfg *config.Config, names []string) ([]Holder, error) {
	var accounts, own []string
	seen := map[string]bool{}
	add := func(list *[]string, key, name string) {
		if !seen[key] {
			seen[key] = true
			*list = append(*list, name)
		}
	}
	addProvider := func(n string) {
		if a := cfg.Providers[n].Account; a != "" {
			add(&accounts, "account:"+a, a)
		} else {
			add(&own, "provider:"+n, n)
		}
	}
	if len(names) == 0 {
		names = append(cfg.AccountNames(), cfg.Names()...)
	}
	for _, n := range names {
		p, isProvider := cfg.Providers[n]
		_, isAccount := cfg.Accounts[n]
		switch {
		case isProvider && p.IsPool():
			for _, m := range cfg.Leaves(n) {
				addProvider(m)
			}
		case isProvider:
			addProvider(n)
		case isAccount:
			add(&accounts, "account:"+n, n)
		default:
			return nil, fmt.Errorf("no provider or account %q", n)
		}
	}
	out := make([]Holder, 0, len(accounts)+len(own))
	for _, a := range accounts {
		p, ok := cfg.AccountProvider(a)
		if !ok {
			continue
		}
		out = append(out, Holder{Name: a, Account: true, Provider: p, Users: cfg.UsersOf(a)})
	}
	for _, n := range own {
		out = append(out, Holder{Name: n, Provider: cfg.Providers[n]})
	}
	return out, nil
}
