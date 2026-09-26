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

// Remove deletes a provider, clearing the default if it pointed there.
func Remove(cfg *config.Config, name string) error {
	if _, ok := cfg.Providers[name]; !ok {
		return fmt.Errorf("provider %q does not exist", name)
	}
	return mutate(cfg, func(c *config.Config) {
		delete(c.Providers, name)
		if c.Settings.Default == name {
			c.Settings.Default = ""
		}
	})
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
			NoKey:    p.APIKey == "" && p.APIKeyRef == "",
			NoEngine: !engineFound[p.Kind],
		}
	}
	return out
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
