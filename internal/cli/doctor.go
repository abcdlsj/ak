package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/spf13/cobra"
)

// providerEnvKeys 是会被 settings.json 里的 env 覆盖而失效的供应商专属键。
// claude 启动时执行 Object.assign(process.env, settingsEnv),settings 是后写方,
// 所以这些键一旦残留在 settings.json,对应的 ak-* 命令会被静默覆盖。
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
		Short: "检查环境是否会让 ak 的命令失效",
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

	// 1. settings.json 里的供应商键残留。这是最严重的问题:不报错,只是静默走错供应商。
	residual := residualProviderKeys()
	if len(residual) > 0 {
		problems++
		fmt.Println("✗ ~/.claude/settings.json 的 env 里还有供应商专属键:")
		for _, k := range residual {
			fmt.Printf("    %s\n", k)
		}
		fmt.Println("  这些键会覆盖 ak-* 命令注入的环境变量,导致命令静默连到错误的供应商。")
		fmt.Println("  删除它们即可(建议先 `ak import --from claude-settings` 收进 ak):")
		fmt.Printf("    %s\n", claudeSettingsPath())
		fmt.Println("  全局偏好键(如 NODE_EXTRA_CA_CERTS、CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC)请保留。")
	} else {
		fmt.Println("✓ settings.json 无供应商键残留")
	}

	// 2. bin_dir 在 PATH 里。
	binDir := config.ExpandHome(cfg.Settings.BinDir)
	if !onPath(binDir) {
		problems++
		fmt.Printf("✗ %s 不在 PATH 里,ak-* 命令无法直接调用\n", binDir)
	} else {
		fmt.Printf("✓ %s 在 PATH 里\n", binDir)
	}

	// 3. 引擎可执行文件可解析。
	if hasKind(cfg, config.KindClaude) {
		if p := lookPathOr(cfg.Settings.ClaudeBin, "claude"); p == "" {
			problems++
			fmt.Println("✗ 找不到 claude 可执行文件")
		} else {
			fmt.Printf("✓ claude: %s\n", p)
		}
	}
	if hasKind(cfg, config.KindCodex) {
		if p := lookPathOr(cfg.Settings.CodexBin, "codex"); p == "" {
			problems++
			fmt.Println("✗ 找不到 codex 可执行文件")
		} else {
			fmt.Printf("✓ codex: %s\n", p)
		}
	}

	// 4. 生成的命令是否与磁盘一致。
	drifted, err := driftReport(cfg)
	if err == nil {
		if len(drifted) > 0 {
			problems++
			fmt.Println("✗ 以下命令与配置不一致,跑 `ak sync` 修复:")
			for _, d := range drifted {
				fmt.Printf("    %s\n", filepath.Base(d))
			}
		} else if len(cfg.Providers) > 0 {
			fmt.Println("✓ 所有命令与配置一致")
		}
	}

	// 5. 配置文件权限。
	if path, err := config.Path(); err == nil {
		if fi, err := os.Stat(path); err == nil {
			if fi.Mode().Perm() != 0o600 {
				problems++
				fmt.Printf("✗ %s 权限是 %o,应为 600(内含明文 key)\n", path, fi.Mode().Perm())
			} else {
				fmt.Println("✓ 配置文件权限 600")
			}
		}
	}

	if len(cfg.Providers) == 0 {
		fmt.Println("\n还没有配置供应商。`ak add` 或 `ak import --from claude-settings`。")
		return nil
	}

	if problems > 0 {
		fmt.Printf("\n发现 %d 个问题。\n", problems)
		return fmt.Errorf("%d 个问题待处理", problems)
	}
	fmt.Println("\n一切正常。")
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

// lookPath 是 exec.LookPath 的薄封装。
func lookPath(name string) (string, error) { return exec.LookPath(name) }

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0
}

// residualProviderKeys 读出 settings.json env 里残留的供应商键。
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

// onPath 检查 dir 是否出现在 PATH 里。
func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// driftReport 返回内容与配置不一致的产物路径。
// 用 DryRun,doctor 只做检查不落盘。
func driftReport(cfg *config.Config) ([]string, error) {
	s := newSyncer(cfg)
	s.DryRun = true
	rep, err := s.Sync()
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
