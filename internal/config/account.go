package config

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/abcdlsj/ak/internal/vendor"
)

// Account is one subscription or key at a vendor: the credentials, kept once
// and shared by every provider that names it. A provider on an account takes
// its endpoint from the vendor for its engine, so one sign-in serves claude,
// codex and pi alike, and its balance is one balance.
type Account struct {
	Vendor  string `toml:"vendor"`
	Display string `toml:"display,omitempty"`
	// User is who signed in, as the vendor names them, for display.
	User string `toml:"user,omitempty"`
	// APIKey is the plaintext key, kept in auth.json like a provider's.
	APIKey    string `toml:"-"`
	APIKeyRef string `toml:"api_key_ref,omitempty"`
	// Secrets are further credentials a sign-in handed out, such as a
	// control-plane key, read only by the balance query as {{var.NAME}}.
	// They live in auth.json and never reach an engine.
	Secrets map[string]string `toml:"-"`
}

// SetKey stores a key or a key reference, clearing the other.
func (a *Account) SetKey(v string) {
	if IsKeyRef(v) {
		a.APIKey, a.APIKeyRef = "", v
	} else {
		a.APIKey, a.APIKeyRef = v, ""
	}
}

// HasKey reports whether the account can authenticate.
func (a Account) HasKey() bool { return a.APIKey != "" || a.APIKeyRef != "" }

// AccountNames returns the sorted account names.
func (c *Config) AccountNames() []string { return slices.Sorted(maps.Keys(c.Accounts)) }

// UsersOf lists, sorted, the providers on an account.
func (c *Config) UsersOf(account string) []string {
	var out []string
	for _, n := range c.Names() {
		if c.Providers[n].Account == account {
			out = append(out, n)
		}
	}
	return out
}

// Vendor returns the vendor of a provider's account, if it has one.
func (c *Config) Vendor(p Provider) (vendor.Vendor, bool) {
	if p.Account == "" {
		return vendor.Vendor{}, false
	}
	return vendor.Lookup(c.Accounts[p.Account].Vendor)
}

// inherited pairs each provider field an endpoint can set with the
// endpoint's value. Resolve and Stored walk it in opposite directions, so
// the two always agree on what an account provides.
func inherited(p *Provider, e vendor.Endpoint) []struct {
	field *string
	value string
} {
	return []struct {
		field *string
		value string
	}{
		{&p.BaseURL, e.BaseURL}, {&p.Model, e.Model},
		{&p.Haiku, e.Haiku}, {&p.Sonnet, e.Sonnet}, {&p.Opus, e.Opus}, {&p.KeyField, e.KeyField},
		{&p.WireAPI, e.WireAPI}, {&p.PiAPI, e.PiAPI},
	}
}

// Resolve fills every provider on an account with what the account gives
// it: the key and secrets always, and each endpoint field the provider
// leaves unset. It is idempotent; Load runs it, and so must anything that
// changes providers or accounts before validating them.
func (c *Config) Resolve() {
	for name, p := range c.Providers {
		c.Providers[name] = c.resolve(p)
	}
}

func (c *Config) resolve(p Provider) Provider { return c.resolveOn(p, "") }

// resolveOn fills p from its account, taking the endpoint of protocol when one
// is named and otherwise the one its kind asks the vendor for.
func (c *Config) resolveOn(p Provider, protocol string) Provider {
	if p.Account == "" {
		return p
	}
	a := c.Accounts[p.Account]
	p.APIKey, p.APIKeyRef, p.AuthKey, p.Secrets = a.APIKey, a.APIKeyRef, "", a.Secrets
	v, _ := c.Vendor(p)
	p.Quota = cmp.Or(p.Quota, v.Quota)
	var (
		e  vendor.Endpoint
		ok bool
	)
	if protocol != "" {
		e, ok = v.EndpointFor(protocol)
	} else {
		e, ok = v.Endpoint(string(p.Kind))
	}
	if !ok {
		return p
	}
	for _, f := range inherited(&p, e) {
		if *f.field == "" {
			*f.field = f.value
		}
	}
	p.PiAuthHeader = p.PiAuthHeader || e.PiAuthHeader
	if len(e.Env) > 0 {
		env := maps.Clone(e.Env)
		maps.Copy(env, p.Env)
		p.Env = env
	}
	return p
}

// AccountProvider is a provider on the account for its vendor's first
// engine, as the account alone gives it: enough to ask the balance of an
// account no provider runs on yet.
func (c *Config) AccountProvider(name string) (Provider, bool) {
	v, ok := vendor.Lookup(c.Accounts[name].Vendor)
	if !ok || len(v.Kinds()) == 0 {
		return Provider{}, false
	}
	return c.resolve(Provider{Kind: Kind(v.Kinds()[0]), Account: name}), true
}

// Stored is p as providers.toml keeps it: for a provider on an account,
// without the credentials and without each field equal to what the
// account gives, so only the provider's own overrides are written. Any
// other provider is returned unchanged.
func (c *Config) Stored(p Provider) Provider {
	if p.Account == "" {
		return p
	}
	p.APIKey, p.APIKeyRef, p.AuthKey, p.Secrets = "", "", "", nil
	v, _ := c.Vendor(p)
	if p.Quota == v.Quota {
		p.Quota = ""
	}
	e, ok := v.Endpoint(string(p.Kind))
	if !ok {
		return p
	}
	for _, f := range inherited(&p, e) {
		if *f.field == f.value {
			*f.field = ""
		}
	}
	if e.PiAuthHeader {
		p.PiAuthHeader = false
	}
	if len(p.Env) > 0 && len(e.Env) > 0 {
		env := maps.Clone(p.Env)
		maps.DeleteFunc(env, func(k, v string) bool { return e.Env[k] == v })
		p.Env = env
	}
	if len(p.Env) == 0 {
		p.Env = nil
	}
	return p
}

// An account's key is kept in auth.json under accountEntry, each secret
// under accountEntry + ":" + its name. The colon cannot appear in a
// provider's name, so neither can collide with a provider's own entry.
const accountPrefix = "account:"

func accountEntry(name string) string { return accountPrefix + name }

// readAccountAuth takes each account's key and secrets out of auth.json.
func (c *Config) readAccountAuth(auth map[string]string) {
	for name, a := range c.Accounts {
		entry := accountEntry(name)
		a.APIKey = auth[entry]
		a.Secrets = nil
		for k, v := range auth {
			if s, ok := strings.CutPrefix(k, entry+":"); ok && s != "" {
				if a.Secrets == nil {
					a.Secrets = map[string]string{}
				}
				a.Secrets[s] = v
			}
		}
		c.Accounts[name] = a
	}
}

// writeAccountAuth adds every account's key and secrets to auth.
func (c *Config) writeAccountAuth(auth map[string]string) {
	for name, a := range c.Accounts {
		entry := accountEntry(name)
		if a.APIKey != "" {
			auth[entry] = a.APIKey
		}
		for k, v := range a.Secrets {
			auth[entry+":"+k] = v
		}
	}
}
