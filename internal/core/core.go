// Package core holds the operations shared by the CLI and the TUI: loading
// and changing providers, syncing their commands, and checking their health.
package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/abcdlsj/ak/internal/usage"
)

// Load reads and validates the config.
func Load() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Add validates and saves a new provider.
func Add(cfg *config.Config, name string, p config.Provider) error {
	if err := config.ValidateName(name); err != nil {
		return err
	}
	if _, exists := cfg.Providers[name]; exists {
		return fmt.Errorf("provider %q already exists, use `ak edit %s`", name, name)
	}
	return mutate(cfg, func(c *config.Config) { c.Providers[name] = p })
}

// Update validates and saves a change to an existing provider.
func Update(cfg *config.Config, name string, p config.Provider) error {
	if _, ok := cfg.Providers[name]; !ok {
		return fmt.Errorf("provider %q does not exist", name)
	}
	return mutate(cfg, func(c *config.Config) { c.Providers[name] = p })
}

// Remove deletes a provider, clearing the default if it pointed there. A
// provider that is a pool's only member cannot be removed until the pool is.
func Remove(cfg *config.Config, name string) error {
	if _, ok := cfg.Providers[name]; !ok {
		return fmt.Errorf("provider %q does not exist", name)
	}
	if pool, ok := onlyMemberOf(cfg, name); ok {
		return fmt.Errorf("provider %q is the only member of pool %q; remove the pool first", name, pool)
	}
	return mutate(cfg, func(c *config.Config) {
		delete(c.Providers, name)
		if c.Settings.Default == name {
			c.Settings.Default = ""
		}
		dropMember(c, name)
	})
}

// onlyMemberOf reports the pool that names provider as its only member.
func onlyMemberOf(cfg *config.Config, name string) (string, bool) {
	for _, poolName := range cfg.Names() {
		p := cfg.Providers[poolName]
		if p.IsPool() && len(p.Members) == 1 && p.Members[0] == name {
			return poolName, true
		}
	}
	return "", false
}

// dropMember removes a provider from every pool that lists it. The member
// slice and mapping are cloned before use: mutate shares them with the original
// config, so in-place edits would corrupt it if validation later rejects.
func dropMember(c *config.Config, name string) {
	for poolName, p := range c.Providers {
		if !p.IsPool() || !containsMember(p.Members, name) {
			continue
		}
		kept := make([]string, 0, len(p.Members))
		for _, m := range p.Members {
			if m != name {
				kept = append(kept, m)
			}
		}
		p.Members = kept
		p.MemberModels = cloneModels(p.MemberModels)
		delete(p.MemberModels, name)
		c.Providers[poolName] = p
	}
}

// renameMember rewrites pool references when a member is renamed, cloning the
// slices and maps it touches for the same reason as dropMember.
func renameMember(c *config.Config, from, to string) {
	for poolName, p := range c.Providers {
		if !p.IsPool() {
			continue
		}
		changed := false
		if containsMember(p.Members, from) {
			ms := make([]string, len(p.Members))
			copy(ms, p.Members)
			for i := range ms {
				if ms[i] == from {
					ms[i] = to
				}
			}
			p.Members = ms
			changed = true
		}
		if v, ok := p.MemberModels[from]; ok {
			p.MemberModels = cloneModels(p.MemberModels)
			delete(p.MemberModels, from)
			p.MemberModels[to] = v
			changed = true
		}
		if changed {
			c.Providers[poolName] = p
		}
	}
}

func containsMember(members []string, name string) bool {
	for _, m := range members {
		if m == name {
			return true
		}
	}
	return false
}

func cloneModels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Edit saves a change to a provider, renaming it when name differs from orig.
func Edit(cfg *config.Config, orig, name string, p config.Provider) error {
	if err := Update(cfg, orig, p); err != nil {
		return err
	}
	return Rename(cfg, orig, name)
}

// Rename moves a provider to a new name, and so its command to
// <prefix><to>; the next sync reclaims the old command. Usage history follows:
// a codex provider keeps its provider_id, and claude session records are
// rewritten to the new name.
func Rename(cfg *config.Config, from, to string) error {
	p, ok := cfg.Providers[from]
	if !ok {
		return fmt.Errorf("provider %q does not exist", from)
	}
	if from == to {
		return nil
	}
	if err := config.ValidateName(to); err != nil {
		return err
	}
	if _, exists := cfg.Providers[to]; exists {
		return fmt.Errorf("provider %q already exists", to)
	}
	if p.Kind == config.KindCodex {
		p.ProviderID = provider.CodexProviderID(from, p)
	}
	err := mutate(cfg, func(c *config.Config) {
		delete(c.Providers, from)
		c.Providers[to] = p
		if c.Settings.Default == from {
			c.Settings.Default = to
		}
		renameMember(c, from, to)
	})
	if err != nil {
		return err
	}
	return usage.RenameProvider(from, to)
}

// SetDefault marks a provider as the default; an empty name clears it.
func SetDefault(cfg *config.Config, name string) error {
	if _, ok := cfg.Providers[name]; name != "" && !ok {
		return fmt.Errorf("provider %q does not exist", name)
	}
	return mutate(cfg, func(c *config.Config) { c.Settings.Default = name })
}

// mutate applies a change to a copy, validates and saves it, and only then
// replaces cfg, so a rejected change leaves cfg untouched.
func mutate(cfg *config.Config, change func(*config.Config)) error {
	next := *cfg
	next.Providers = make(map[string]config.Provider, len(cfg.Providers)+1)
	for k, v := range cfg.Providers {
		next.Providers[k] = v
	}
	change(&next)
	if err := config.Validate(&next); err != nil {
		return err
	}
	if err := config.Save(&next); err != nil {
		return err
	}
	*cfg = next
	return nil
}

// NewSyncer builds the syncer every caller that generates artifacts uses.
func NewSyncer(cfg *config.Config) *shim.Syncer {
	return &shim.Syncer{Cfg: cfg, Resolver: secrets.Default()}
}

// Sync regenerates every provider's commands.
func Sync(cfg *config.Config, dryRun bool) (shim.Report, error) {
	s := NewSyncer(cfg)
	s.DryRun = dryRun
	return s.Sync()
}

// CommandPath is where a provider's command lives.
func CommandPath(cfg *config.Config, name string) string {
	return filepath.Join(config.ExpandHome(cfg.Settings.BinDir), cfg.Settings.Prefix+name)
}

// Status is a provider's health, as far as can be told without a request.
type Status struct {
	// Drift means the command on disk does not match the config.
	Drift bool
	// NoKey means neither api_key nor api_key_ref is set.
	NoKey bool
	// NoEngine means the engine binary cannot be found.
	NoEngine bool
}

// OK reports whether nothing is wrong.
func (s Status) OK() bool { return !s.Drift && !s.NoKey && !s.NoEngine }

// Problems describes what is wrong, most urgent first.
func (s Status) Problems() []string {
	var out []string
	if s.NoEngine {
		out = append(out, "engine not found")
	}
	if s.NoKey {
		out = append(out, "no API key")
	}
	if s.Drift {
		out = append(out, "out of sync, run ak sync")
	}
	return out
}

// Statuses checks every provider. It only reads: drift comes from a dry-run
// sync, which defers api_key_ref lookups and so never prompts a keychain.
func Statuses(cfg *config.Config) map[string]Status {
	out := map[string]Status{}
	drift := map[string]bool{}
	if rep, err := Sync(cfg, true); err == nil {
		for _, r := range rep.Results {
			if r.Action == shim.ActionCreated || r.Action == shim.ActionUpdated {
				drift[r.Path] = true
			}
		}
	}
	engineFound := map[config.Kind]bool{}
	for _, eng := range provider.Engines() {
		engineFound[eng.Kind()] = findEngine(cfg, eng) != ""
	}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		out[name] = Status{
			Drift:    drift[CommandPath(cfg, name)],
			NoKey:    noKey(cfg, p),
			NoEngine: !engineFound[p.Kind],
		}
	}
	return out
}

// noKey reports whether a provider cannot authenticate. A pool holds no key of
// its own; it is missing one only when a member is.
func noKey(cfg *config.Config, p config.Provider) bool {
	if p.IsPool() {
		for _, m := range p.Members {
			mp := cfg.Providers[m]
			if mp.APIKey == "" && mp.APIKeyRef == "" {
				return true
			}
		}
		return false
	}
	return p.APIKey == "" && p.APIKeyRef == ""
}

// findEngine returns the engine binary the commands would run, or "".
func findEngine(cfg *config.Config, eng provider.Engine) string {
	if c := eng.ConfiguredBin(cfg.Settings); c != "" {
		p := config.ExpandHome(c)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath(eng.BinName()); err == nil {
		return p
	}
	return ""
}
