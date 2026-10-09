package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/launch"
	"github.com/abcdlsj/ak/internal/recent"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/abcdlsj/ak/internal/usage"
)

// runTarget launches <provider>[:variant] with the engine arguments args.
func runTarget(target string, args []string) error {
	name, variant, err := launch.ParseTarget(target)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	return execProvider(cfg, name, variant, args)
}

// runLauncher opens the launcher on the provider last used here, then
// launches what was picked.
func runLauncher() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	sel, err := ui.RunUI(cfg, launcherFocus(cfg))
	if err != nil || sel.Provider == "" {
		return err
	}
	return execProvider(cfg, sel.Provider, sel.Variant, nil)
}

// launcherFocus is the provider last launched in this directory or its
// repository, else the default.
func launcherFocus(cfg *config.Config) string {
	if s, err := recent.Default(); err == nil {
		if wd, err := os.Getwd(); err == nil {
			exists := func(n string) bool { _, ok := cfg.Providers[n]; return ok }
			if n := s.Lookup(wd, exists); n != "" {
				return n
			}
		}
	}
	return cfg.Settings.Default
}

// execProvider replaces ak with the provider's engine. What it cannot launch
// is caught here, with the fix, rather than surfacing later as the engine's
// own error (a 401 for a missing key).
func execProvider(cfg *config.Config, name, variant string, args []string) error {
	p, ok := cfg.Providers[name]
	if !ok {
		return fmt.Errorf("provider %q does not exist; see `ak list`", name)
	}
	if core.QuickStatus(cfg, name).NoKey {
		if p.IsPool() {
			return fmt.Errorf("a member of pool %s has no API key; see `ak list`", name)
		}
		return fmt.Errorf("%s has no API key; set one with `ak edit %s --key -`", name, name)
	}
	key, err := secrets.Default().Resolve(p)
	if err != nil {
		return fmt.Errorf("resolve the key for %s: %w", name, err)
	}
	o := launch.Options{Environ: os.Environ(), Key: key}
	if p.Kind == config.KindClaude {
		if id, err := launch.NewSessionID(); err == nil {
			o.SessionID = id
		}
	}
	plan, err := launch.Build(cfg, name, variant, args, o)
	if err != nil {
		return err
	}
	if plan.Pool {
		if err := ensureGateway(gatewayAddr(cfg)); err != nil {
			return err
		}
	}
	// Bookkeeping never blocks a launch.
	if plan.SessionID != "" {
		if err := usage.RecordSession(plan.SessionID, name); err != nil {
			warnf("ak: record session: %v", err)
		}
	}
	if s, err := recent.Default(); err == nil {
		if wd, err := os.Getwd(); err == nil {
			_ = s.Remember(wd, name)
		}
	}
	// syscall.Exec lets the engine take over the terminal and process,
	// leaving no intermediate process behind.
	return syscall.Exec(plan.Bin, plan.Argv, plan.Env)
}
