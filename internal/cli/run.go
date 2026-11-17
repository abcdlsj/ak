package cli

import (
	"fmt"
	"os"
	"strings"
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

// withoutOverride drops the one-shot override from environ.
func withoutOverride(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if !strings.HasPrefix(kv, overrideEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// launchRefusal says why this provider cannot be launched, or nil. A sign-in
// launch is exempt from a missing credential, but from nothing else: an engine
// that takes a key still needs one during the launch that would add it.
func launchRefusal(name string, p config.Provider, st core.Status, signIn bool) error {
	if st.NeedsLogin && !signIn {
		return fmt.Errorf("%s is not signed in; run `ak agy login %s` and finish the sign-in it opens", name, name)
	}
	if st.NoKey {
		if p.IsPool() {
			return fmt.Errorf("a member of pool %s has no API key; see `ak list`", name)
		}
		return fmt.Errorf("%s has no API key; set one with `ak edit %s --key -`", name, name)
	}
	return nil
}

// execProvider replaces ak with the provider's engine. What it cannot launch
// is caught here, with the fix, rather than surfacing later as the engine's
// own error (a 401 for a missing key).
func execProvider(cfg *config.Config, name, variant string, args []string) error {
	return execProviderWith(cfg, name, variant, args, false)
}

// execProviderToSignIn launches an engine that signs in itself even though no
// credential is there yet: the launch is what writes one, so the check every
// other launch makes would refuse the very command that fixes it.
func execProviderToSignIn(cfg *config.Config, name, variant string, args []string) error {
	return execProviderWith(cfg, name, variant, args, true)
}

func execProviderWith(cfg *config.Config, name, variant string, args []string, signIn bool) error {
	p, ok := cfg.Providers[name]
	if !ok {
		return fmt.Errorf("provider %q does not exist; see `ak list`", name)
	}
	if err := launchRefusal(name, p, core.QuickStatus(cfg, name), signIn); err != nil {
		return err
	}
	key, err := secrets.Default().Resolve(p)
	if err != nil {
		return fmt.Errorf("resolve the key for %s: %w", name, err)
	}
	// The one-shot override picked this launch; the engine's children must
	// not inherit it.
	o := launch.Options{Environ: withoutOverride(os.Environ()), Key: key}
	if p.Kind == config.KindClaude {
		if id, err := launch.NewSessionID(); err == nil {
			o.SessionID = id
		}
	}
	plan, err := launch.Build(cfg, name, variant, args, o)
	if err != nil {
		return err
	}
	if home := p.HomeDir(); home != "" {
		if err := os.MkdirAll(config.ExpandHome(home), 0o700); err != nil {
			return fmt.Errorf("prepare %s directory %s: %w", name, home, err)
		}
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
