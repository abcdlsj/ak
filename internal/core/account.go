package core

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/login"
)

// SetAccount adds an account or replaces one, and with it what every
// provider on it runs with.
func SetAccount(cfg *config.Config, name string, a config.Account) error {
	if err := config.ValidateAccountName(name); err != nil {
		return err
	}
	if old, ok := cfg.Accounts[name]; ok && old.Vendor != a.Vendor && len(cfg.UsersOf(name)) > 0 {
		return fmt.Errorf("account %q is on %s and used by %s; sign in to %s under another name",
			name, old.Vendor, strings.Join(cfg.UsersOf(name), ", "), a.Vendor)
	}
	return mutate(cfg, func(c *config.Config) { c.Accounts[name] = a })
}

// RemoveAccount deletes an account no provider runs on.
func RemoveAccount(cfg *config.Config, name string) error {
	if _, ok := cfg.Accounts[name]; !ok {
		return fmt.Errorf("account %q does not exist", name)
	}
	if users := cfg.UsersOf(name); len(users) > 0 {
		return fmt.Errorf("account %q is used by %s; remove or move them first", name, strings.Join(users, ", "))
	}
	return mutate(cfg, func(c *config.Config) { delete(c.Accounts, name) })
}

// RenameAccount moves an account to a new name, and its providers with it.
func RenameAccount(cfg *config.Config, from, to string) error {
	a, ok := cfg.Accounts[from]
	if !ok {
		return fmt.Errorf("account %q does not exist", from)
	}
	if from == to {
		return nil
	}
	if err := config.ValidateAccountName(to); err != nil {
		return err
	}
	if _, exists := cfg.Accounts[to]; exists {
		return fmt.Errorf("account %q already exists", to)
	}
	return mutate(cfg, func(c *config.Config) {
		delete(c.Accounts, from)
		c.Accounts[to] = a
		for _, n := range c.UsersOf(from) {
			p := c.Providers[n]
			p.Account = to
			c.Providers[n] = p
		}
	})
}

// SignIn stores what a sign-in to vendorID handed out as account name,
// creating the account or replacing its credentials. A new key brings its
// own secrets: the old ones belonged to the old key.
func SignIn(cfg *config.Config, name, vendorID string, c login.Credentials) error {
	a := cfg.Accounts[name]
	a.Vendor = vendorID
	a.SetKey(c.Key)
	a.Secrets = c.Secrets
	if c.User != "" {
		a.User = c.User
	}
	return SetAccount(cfg, name, a)
}
