// Package cli is the command-line entry point for ak.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/spf13/cobra"
)

// Version is injected at build time.
var Version = "dev"

func init() {
	// Every command name is reserved, so `ak <name>` always means one thing.
	for _, c := range newRoot().Commands() {
		config.Reserve(c.Name())
		config.Reserve(c.Aliases...)
	}
}

// Execute is the program's main entry point.
func Execute() error {
	// `ak <provider>[:variant] ...` and `ak run ...` are handled before cobra,
	// so the engine's own flags (`ak kimi -p "..."`) reach the engine untouched.
	if args := os.Args[1:]; len(args) > 0 {
		if args[0] == "run" {
			if len(args) < 2 {
				return fmt.Errorf("usage: ak run <provider>[:variant] [args...]")
			}
			return runTarget(args[1], args[2:])
		}
		if isTarget(args[0]) {
			return runTarget(args[0], args[1:])
		}
	}
	return newRoot().Execute()
}

// isTarget reports whether arg names a provider rather than a subcommand.
// Provider names cannot start with "-" or "_" and never collide with a
// subcommand, so only a configured name is a target.
func isTarget(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "_") || config.Reserved[arg] {
		return false
	}
	name, _, _ := strings.Cut(arg, ":")
	cfg, err := loadConfig()
	if err != nil {
		// Not a subcommand, so most likely a provider: let the launch report
		// why the config does not load.
		return true
	}
	_, ok := cfg.Providers[name]
	return ok
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "ak [provider[:variant] [args...]]",
		Short: "Parallel claude / codex / pi launcher, one command per provider",
		Long: `ak launches claude, codex or pi against a provider: ak kimi, ak kimi:high,
ak kimi -p "...". Providers sit side by side rather than being mutually
exclusive, which suits running multiple worktrees, sessions and agents in
parallel. Each provider also gets an ak-<name> command for scripts.

Run with no arguments to open the launcher.`,
		Version:           Version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: completeTarget,
		// No arguments means open the launcher. A provider never reaches here:
		// Execute launches it before cobra parses anything.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command or provider %q; see `ak list`", args[0])
			}
			return runLauncher()
		},
	}

	root.AddCommand(
		newListCmd(),
		newAddCmd(),
		newEditCmd(),
		newRemoveCmd(),
		newRenameCmd(),
		newSyncCmd(),
		newDoctorCmd(),
		newImportCmd(),
		newDefaultCmd(),
		newEnvCmd(),
		newUsageCmd(),
		newUICmd(),
		newPruneCodexaCmd(),
		newHookCmd(),
		newRecordSessionCmd(),
		newServeCmd(),
		newQuotaCmd(),
		newCheckCmd(),
		newModelsCmd(),
		newPresetCmd(),
		newStatusCmd(),
	)
	return root
}

// loadConfig loads and validates the config.
func loadConfig() (*config.Config, error) { return core.Load() }

// warnf writes a warning to stderr.
func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
