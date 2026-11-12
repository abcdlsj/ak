package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	var dryRun, verbose bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Regenerate all commands from the config and reclaim stale artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runSyncVerbose(cfg, dryRun, verbose)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Show the changes without writing anything")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "List every file created, updated or removed")
	return cmd
}

// runSync regenerates the commands and prints the report.
func runSync(cfg *config.Config, dryRun bool) error {
	return runSyncVerbose(cfg, dryRun, false)
}

// runSyncVerbose regenerates the commands and prints the report, naming every
// changed path when verbose.
func runSyncVerbose(cfg *config.Config, dryRun, verbose bool) error {
	rep, err := core.Sync(cfg, dryRun)
	if err != nil {
		return err
	}
	printReport(rep, verbose)
	return nil
}

// printReport says what a sync changed: one Done line of counts, or a Hint
// when nothing changed. A skipped artifact is always named with its reason;
// the paths themselves are named only by -v or a dry run.
func printReport(rep shim.Report, verbose bool) {
	if rep.DryRun {
		out.Hint(os.Stdout, "dry run, nothing written")
	}
	for _, r := range rep.Results {
		if r.Action == shim.ActionSkipped {
			fmt.Printf("%s %s  %s\n", out.Mark(out.Notice), filepath.Base(r.Path), out.Reason(r.Reason))
		}
	}
	if verbose || rep.DryRun {
		for _, r := range rep.Results {
			if artifactChanged(r.Action) {
				fmt.Printf("%s %s\n", out.Dim(string(r.Action)), out.Dim(shortPath(r.Path)))
			}
		}
	}
	c := rep.Counts()
	if c[shim.ActionCreated]+c[shim.ActionUpdated]+c[shim.ActionRemoved] == 0 {
		out.Hint(os.Stdout, "commands already up to date")
		return
	}
	out.Done(os.Stdout, "synced: %s", countList(c))
}

// artifactChanged reports whether a result is a file ak wrote or removed.
func artifactChanged(a shim.Action) bool {
	return a == shim.ActionCreated || a == shim.ActionUpdated || a == shim.ActionRemoved
}

// countList reads "1 created, 2 updated, 1 removed", leaving out zero counts.
func countList(c map[shim.Action]int) string {
	var parts []string
	for _, a := range []shim.Action{shim.ActionCreated, shim.ActionUpdated, shim.ActionRemoved} {
		if n := c[a]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, a))
		}
	}
	return strings.Join(parts, ", ")
}

// shortPath is p with the home directory as "~", so a line stays short.
func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return "~/" + filepath.ToSlash(rel)
}
