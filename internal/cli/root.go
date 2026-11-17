// Package cli is the command-line entry point for ak.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/version"
	"github.com/spf13/cobra"
)

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
		Short: "Parallel claude / codex / pi / agy launcher, one command per provider",
		Long: `ak launches claude, codex, pi or agy against a provider: ak kimi,
ak kimi:high, ak kimi -p "...". Providers sit side by side rather than being
mutually exclusive, which suits running multiple worktrees, sessions and agents
in parallel. Each provider also gets an ak-<name> command for scripts.

Run with no arguments to open the launcher.`,
		Version:           version.Version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeTarget,
		// No arguments means open the launcher. A provider never reaches here:
		// Execute launches it before cobra parses anything.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				if best := suggestTargetOrCommand(cmd, args[0]); best != "" {
					return fmt.Errorf("unknown command or provider %q; did you mean %q?", args[0], best)
				}
				return fmt.Errorf("unknown command or provider %q; see `ak list`", args[0])
			}
			return runLauncher()
		},
	}

	root.AddGroup(
		&cobra.Group{ID: "launch", Title: "Launch and bindings:"},
		&cobra.Group{ID: "providers", Title: "Providers and accounts:"},
		&cobra.Group{ID: "status", Title: "Health, balance and usage:"},
		&cobra.Group{ID: "service", Title: "Gateway and service:"},
		&cobra.Group{ID: "setup", Title: "Shell and repair:"},
	)

	root.AddCommand(withGroup("launch",
		newUICmd(),
		newListCmd(),
		newShowCmd(),
		newBindCmd(),
		newUnbindCmd(),
		newBindingsCmd(),
		newDefaultCmd(),
		newWhichCmd(),
		newEnvCmd(),
		newACPCmd(),
	)...)
	root.AddCommand(withGroup("providers",
		newAddCmd(),
		newEditCmd(),
		newRemoveCmd(),
		newRenameCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newAccountsCmd(),
		newAgyCmd(),
		newVendorCmd(),
		newImportCmd(),
	)...)
	root.AddCommand(withGroup("status",
		newStatusCmd(),
		newQuotaCmd(),
		newCheckCmd(),
		newModelsCmd(),
		newUsageCmd(),
		newDoctorCmd(),
	)...)
	root.AddCommand(withGroup("service",
		newServeCmd(),
		newRemoteCmd(),
	)...)
	root.AddCommand(withGroup("setup",
		newInstallCmd(),
		newUninstallCmd(),
		newInitCmd(),
		newHookCmd(),
		newSyncCmd(),
	)...)
	root.AddCommand(
		newRecordSessionCmd(),
		newResolveCmd(),
		newEngineCmd(),
	)
	return root
}

func withGroup(groupID string, cmds ...*cobra.Command) []*cobra.Command {
	for _, c := range cmds {
		c.GroupID = groupID
	}
	return cmds
}

// suggestTargetOrCommand finds the closest command or provider name to arg,
// returning "" when nothing is close enough.
func suggestTargetOrCommand(root *cobra.Command, arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		return ""
	}
	var cands []string
	for _, c := range root.Commands() {
		if c.Hidden {
			continue
		}
		cands = append(cands, c.Name())
		cands = append(cands, c.Aliases...)
	}
	if cfg, err := loadConfig(); err == nil {
		cands = append(cands, cfg.Names()...)
	}

	best := ""
	bestDist := 3 // only suggest edit distance <= 2
	for _, cand := range cands {
		d := levenshtein(arg, strings.ToLower(cand))
		if d < bestDist || (d == bestDist && best != "" && cand < best) {
			bestDist, best = d, cand
		}
	}
	if best != "" {
		return best
	}
	// Fall back to unique prefix match when arg is at least 3 chars.
	if len(arg) >= 3 {
		var match string
		for _, cand := range cands {
			if strings.HasPrefix(strings.ToLower(cand), arg) {
				if match != "" && match != cand {
					return ""
				}
				match = cand
			}
		}
		return match
	}
	return ""
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

// loadConfig loads and validates the config.
func loadConfig() (*config.Config, error) { return core.Load() }

// warnf writes a warning to stderr.
func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
