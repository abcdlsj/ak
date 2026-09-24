// Package cli 是 ak 的命令行入口。
package cli

import (
	"fmt"
	"os"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/spf13/cobra"
)

// Version 由构建时注入。
var Version = "dev"

// Execute 是程序主入口。
func Execute() error {
	root := &cobra.Command{
		Use:   "ak",
		Short: "多供应商并列的 claude / codex 启动器",
		Long: `ak 为每个供应商生成一个独立命令(ak-kimi、ak-cpa),
供应商之间并列而非互斥,适合多 worktree / 多 session 并行。

无参数运行进入 TUI。`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// 无参数时进 TUI。
		RunE: func(cmd *cobra.Command, args []string) error {
			return ui.RunUI()
		},
	}

	root.AddCommand(
		newListCmd(),
		newAddCmd(),
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
	)
	return root.Execute()
}

// loadConfig 读取并校验配置。
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

// warnf 往 stderr 打提示。
func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
