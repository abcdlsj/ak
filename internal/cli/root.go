// Package cli is the command-line entry point for ak.
package cli

import (
	"fmt"
	"os"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/spf13/cobra"
)

// Version is injected at build time.
var Version = "dev"

// Execute is the program's main entry point.
func Execute() error {
	root := &cobra.Command{
		Use:   "ak",
		Short: "Parallel claude / codex launcher with one command per provider",
		Long: `ak generates one command per provider (ak-kimi, ak-cpa). Providers sit
side by side rather than being mutually exclusive, which suits running
multiple worktrees, sessions and agents in parallel.

Run with no arguments to open the TUI.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// No arguments means open the TUI.
		RunE: func(cmd *cobra.Command, args []string) error {
			return ui.RunUI()
		},
	}

	root.AddCommand(
		newListCmd(),
		newAddCmd(),
		newEditCmd(),
		newRemoveCmd(),
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
		newKeyCmd(),
	)
	return root.Execute()
}

// loadConfig loads and validates the config.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// warnf writes a warning to stderr.
func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
