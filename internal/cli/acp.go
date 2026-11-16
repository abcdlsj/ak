package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/abcdlsj/ak/internal/bind"
	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/launch"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

// acpServers maps an engine to the ACP server ak launches for it. Only engines
// whose adapter reads the provider environment are listed: a provider family
// that configures its engine through arguments (codex) has nothing to gain
// from this path yet, and silently launching the adapter against the wrong
// provider would be worse than saying so.
var acpServers = map[config.Kind]string{
	config.KindClaude: "claude-agent-acp",
}

// acpBinEnv names the variable overriding an engine's ACP server executable.
func acpBinEnv(k config.Kind) string {
	return "AK_" + strings.ToUpper(string(k)) + "_ACP_BIN"
}

func newACPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "acp <engine> [args...]",
		Short: "Run a provider's ACP server with that provider's environment",
		Long: `acp starts the Agent Client Protocol server of an engine family with the
provider that engine resolves to in this directory. It is the ACP counterpart
of the shell functions ak init installs: a client that would otherwise launch
the adapter directly runs "ak acp claude", and the adapter sees the provider's
credentials.

The provider is chosen the same way a terminal launch chooses it: the AK
override, then a repository binding, then the engine default, and the engine's
own environment when none applies.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, ok := parseKind(args[0])
			if !ok {
				return fmt.Errorf("unknown engine %q; one of %s", args[0], kindList())
			}
			return runACP(k, args[1:])
		},
	}
}

// runACP replaces ak with the engine's ACP server. The provider's environment
// comes from the same launch plan its own command would get, so whatever the
// provider needs to authenticate reaches the adapter.
func runACP(k config.Kind, args []string) error {
	server, ok := acpServers[k]
	if !ok {
		return fmt.Errorf("acp is not available for %s", k)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	bin, err := findACPBin(k, server)
	if err != nil {
		return err
	}
	store, err := bind.Default()
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	r, err := resolveEngine(cfg, store, k, wd)
	if err != nil {
		return fmt.Errorf("%w; fix it with `ak bind`, `ak unbind` or `ak default %s`", err, k)
	}
	env := withoutOverride(os.Environ())
	if r.Target != "" {
		env, err = acpProviderEnv(cfg, k, r.Target, env)
		if err != nil {
			return err
		}
	}
	argv := append([]string{bin}, args...)
	return syscall.Exec(bin, argv, env)
}

// acpProviderEnv builds the environment the adapter passes on to the engine.
// The launch plan is used for its environment only: the engine's arguments
// belong to its own command, not to the ACP server.
func acpProviderEnv(cfg *config.Config, k config.Kind, target string, environ []string) ([]string, error) {
	name, variant, err := launch.ParseTarget(target)
	if err != nil {
		return nil, err
	}
	p, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q does not exist; see `ak list`", name)
	}
	if core.QuickStatus(cfg, name).NeedsLogin {
		return nil, fmt.Errorf("%s is not signed in; run `ak agy login %s` and finish the sign-in it opens", name, name)
	}
	if core.QuickStatus(cfg, name).NoKey {
		if p.IsPool() {
			return nil, fmt.Errorf("a member of pool %s has no API key; see `ak list`", name)
		}
		return nil, fmt.Errorf("%s has no API key; set one with `ak edit %s --key -`", name, name)
	}
	key, err := secrets.Default().Resolve(p)
	if err != nil {
		return nil, fmt.Errorf("resolve the key for %s: %w", name, err)
	}
	plan, err := launch.Build(cfg, name, variant, nil, launch.Options{Environ: environ, Key: key})
	if err != nil {
		return nil, err
	}
	if plan.Pool {
		if err := ensureGateway(gatewayAddr(cfg)); err != nil {
			return nil, err
		}
	}
	if k == config.KindClaude {
		// The adapter starts the engine's own CLI. Pin it to the binary this
		// provider was configured to use, so a wrapper's environment and the
		// engine it starts stay one provider.
		return withEnv(plan.Env, "CLAUDE_CODE_EXECUTABLE", plan.Bin), nil
	}
	return plan.Env, nil
}

// findACPBin resolves the ACP server executable: the override variable, then
// PATH.
func findACPBin(k config.Kind, server string) (string, error) {
	if override := os.Getenv(acpBinEnv(k)); override != "" {
		info, err := os.Stat(override)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%s=%s is not an executable", acpBinEnv(k), override)
		}
		return override, nil
	}
	if path, err := exec.LookPath(server); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("%s not found; install the ACP adapter for %s or set %s", server, k, acpBinEnv(k))
}

// withEnv replaces key in an environment list, keeping its position, or
// appends it when absent.
func withEnv(environ []string, key, value string) []string {
	prefix := key + "="
	entry := prefix + value
	found := false
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if strings.HasPrefix(kv, prefix) {
			if !found {
				out = append(out, entry)
				found = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, entry)
	}
	return out
}
