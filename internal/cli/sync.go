package cli

import (
	"fmt"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Regenerate all commands from the config and reclaim stale artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runSync(cfg, dryRun)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Show the changes without writing anything")
	return cmd
}

func runSync(cfg *config.Config, dryRun bool) error {
	rep, err := core.Sync(cfg, dryRun)
	if err != nil {
		return err
	}
	printReport(rep)
	return nil
}

func printReport(rep shim.Report) {
	if rep.DryRun {
		fmt.Println("(dry-run, nothing written)")
	}
	for _, r := range rep.Results {
		switch r.Action {
		case shim.ActionUnchanged:
			continue // Stay quiet about unchanged files.
		case shim.ActionSkipped:
			fmt.Printf("  %-9s %s  — %s\n", r.Action, filepath.Base(r.Path), r.Reason)
		default:
			fmt.Printf("  %-9s %s\n", r.Action, r.Path)
		}
	}
	c := rep.Counts()
	fmt.Printf("%d created, %d updated, %d unchanged, %d removed, %d skipped\n",
		c[shim.ActionCreated], c[shim.ActionUpdated],
		c[shim.ActionUnchanged], c[shim.ActionRemoved], c[shim.ActionSkipped])
}
