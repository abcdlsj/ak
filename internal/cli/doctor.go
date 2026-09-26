package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/spf13/cobra"
)

// providerEnvKeys are the provider-specific keys that stop working when they
// remain in settings.json. Claude Code runs Object.assign(process.env,
// settingsEnv) at startup, so settings.json wins over the environment. Any of
// these keys left behind silently override the matching ak-* command.
var providerEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check for conditions that would make ak commands misroute",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runDoctor(cfg)
		},
	}
	return cmd
}

func runDoctor(cfg *config.Config) error {
	problems := 0

	// 1. Provider keys left in settings.json. This is the worst case: no error
	// is raised, requests just silently go to the wrong provider.
	residual := residualProviderKeys()
	if len(residual) > 0 {
		problems++
		fmt.Println("✗ ~/.claude/settings.json still has provider-specific keys in env:")
		for _, k := range residual {
			fmt.Printf("    %s\n", k)
		}
		fmt.Println("  These override the environment variables ak-* commands inject, so")
		fmt.Println("  those commands silently reach the wrong provider.")
		fmt.Println("  Remove them (run `ak import --from claude-settings` first to keep them):")
		fmt.Printf("    %s\n", claudeSettingsPath())
		fmt.Println("  Keep the global preference keys, e.g. NODE_EXTRA_CA_CERTS and")
		fmt.Println("  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC.")
	} else {
		fmt.Println("✓ no provider keys left in settings.json")
	}

	// 2. bin_dir must be on PATH.
	binDir := config.ExpandHome(cfg.Settings.BinDir)
	if !onPath(binDir) {
		problems++
		fmt.Printf("✗ %s is not on PATH, ak-* commands cannot be invoked directly\n", binDir)
	} else {
		fmt.Printf("✓ %s is on PATH\n", binDir)
	}

	// 3. Engine binaries must resolve.
	if hasKind(cfg, config.KindClaude) {
		if p := lookPathOr(cfg.Settings.ClaudeBin, "claude"); p == "" {
			problems++
			fmt.Println("✗ claude executable not found")
		} else {
			fmt.Printf("✓ claude: %s\n", p)
		}
	}
	if hasKind(cfg, config.KindCodex) {
		if p := lookPathOr(cfg.Settings.CodexBin, "codex"); p == "" {
			problems++
			fmt.Println("✗ codex executable not found")
		} else {
			fmt.Printf("✓ codex: %s\n", p)
		}
	}

	// 4. Generated commands must match the config on disk.
	drifted, err := driftReport(cfg)
	if err == nil {
		if len(drifted) > 0 {
			problems++
			fmt.Println("✗ these commands do not match the config, run `ak sync` to fix:")
			for _, d := range drifted {
				fmt.Printf("    %s\n", filepath.Base(d))
			}
		} else if len(cfg.Providers) > 0 {
			fmt.Println("✓ all commands match the config")
		}
	}

	// 5. Claude usage attribution needs the SessionStart hook.
	if hasKind(cfg, config.KindClaude) {
		if hookInstalled() {
			fmt.Println("✓ usage attribution hook installed")
		} else {
			fmt.Println("! usage attribution hook missing; claude usage shows as unknown. Run `ak hook install`")
		}
	}

	// 6. Config file permissions.
	if path, err := config.Path(); err == nil {
		if fi, err := os.Stat(path); err == nil {
			if fi.Mode().Perm() != 0o600 {
				problems++
				fmt.Printf("✗ %s has mode %o, expected 600 (it holds plaintext keys)\n", path, fi.Mode().Perm())
			} else {
				fmt.Println("✓ config file mode is 600")
			}
		}
	}

	if len(cfg.Providers) == 0 {
		fmt.Println("\nNo providers configured yet. Use `ak add` or `ak import --from claude-settings`.")
		return nil
	}

	if problems > 0 {
		fmt.Printf("\nFound %d problem(s).\n", problems)
		return fmt.Errorf("%d problem(s) need attention", problems)
	}
	fmt.Println("\nAll checks passed.")
	return nil
}

func hasKind(cfg *config.Config, k config.Kind) bool {
	for _, p := range cfg.Providers {
		if p.Kind == k {
			return true
		}
	}
	return false
}

func lookPathOr(configured, name string) string {
	if configured != "" {
		p := config.ExpandHome(configured)
		if isExecutable(p) {
			return p
		}
	}
	if p, err := lookPath(name); err == nil {
		return p
	}
	return ""
}

// lookPath is a thin wrapper around exec.LookPath.
func lookPath(name string) (string, error) { return exec.LookPath(name) }

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0
}

// residualProviderKeys returns provider keys still present in settings.json's env.
func residualProviderKeys() []string {
	data, err := os.ReadFile(claudeSettingsPath())
	if err != nil {
		return nil
	}
	var settings struct {
		Env map[string]json.RawMessage `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	var out []string
	for _, k := range providerEnvKeys {
		if _, ok := settings.Env[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

func claudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// onPath reports whether dir appears in PATH.
func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// driftReport returns the paths of generated artifacts that no longer match
// the config. DryRun is set so doctor only inspects and never writes.
func driftReport(cfg *config.Config) ([]string, error) {
	rep, err := core.Sync(cfg, true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rep.Results {
		switch r.Action {
		case shim.ActionUpdated, shim.ActionCreated:
			out = append(out, r.Path)
		}
	}
	return out, nil
}
