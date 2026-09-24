package cli

import (
	"fmt"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "按配置重新生成所有命令,并回收不再需要的产物",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runSync(cfg, dryRun)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "只显示将要做的改动,不落盘")
	return cmd
}

func runSync(cfg *config.Config, dryRun bool) error {
	s := newSyncer(cfg)
	s.DryRun = dryRun
	rep, err := s.Sync()
	if err != nil {
		return err
	}
	printReport(rep)
	return nil
}

// newSyncer 构造同步器。所有需要生成产物的调用方都用它。
func newSyncer(cfg *config.Config) *shim.Syncer {
	return &shim.Syncer{Cfg: cfg, Resolver: secrets.Default()}
}

func printReport(rep shim.Report) {
	if rep.DryRun {
		fmt.Println("(dry-run,未落盘)")
	}
	for _, r := range rep.Results {
		switch r.Action {
		case shim.ActionUnchanged:
			continue // 不变的不打,输出保持安静
		case shim.ActionSkipped:
			fmt.Printf("  %-9s %s  — %s\n", r.Action, filepath.Base(r.Path), r.Reason)
		default:
			fmt.Printf("  %-9s %s\n", r.Action, r.Path)
		}
	}
	c := rep.Counts()
	fmt.Printf("%d 新建, %d 更新, %d 未变, %d 删除, %d 跳过\n",
		c[shim.ActionCreated], c[shim.ActionUpdated],
		c[shim.ActionUnchanged], c[shim.ActionRemoved], c[shim.ActionSkipped])
}
