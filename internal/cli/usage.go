package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/usage"
	"github.com/spf13/cobra"
)

func newUsageCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "统计 claude / codex 的 token 用量",
		Long: `解析本机 session 日志统计用量。

首次扫描需要解析全部历史日志,耗时数秒;之后增量更新,近乎瞬时。
成本按 models.dev 定价估算,查不到定价的模型只计 token 不计钱。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			sum, err := usage.Aggregate(cfg)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(sum)
			}
			printUsage(sum)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

func printUsage(s usage.Summary) {
	fmt.Printf("总计 %s tokens", humanizeInt(s.TotalTokens))
	if s.TotalCost > 0 {
		fmt.Printf("  ·  约 $%.2f", s.TotalCost)
	}
	if s.UnpricedTokens > 0 {
		fmt.Printf("  (%s tokens 无定价数据,未计入成本)", humanizeInt(s.UnpricedTokens))
	}
	fmt.Println()

	if len(s.ByProvider) > 0 {
		fmt.Println("按供应商")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  供应商\ttokens\t成本")
		for _, r := range s.ByProvider {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Name, humanizeInt(r.Tokens), costStr(r.Cost))
		}
		w.Flush()
		fmt.Println()
	}

	if len(s.ByModel) > 0 {
		fmt.Println("按模型")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  模型\ttokens\t成本")
		for _, r := range s.ByModel {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Model, humanizeInt(r.Tokens), costStr(r.Cost))
		}
		w.Flush()
		fmt.Println()
	}

	if len(s.ByDate) > 0 {
		fmt.Printf("近 %d 天有记录,最早 %s,最晚 %s。\n",
			len(s.ByDate), s.ByDate[0].Date, s.ByDate[len(s.ByDate)-1].Date)
	}
}

func costStr(c float64) string {
	if c <= 0 {
		return "-"
	}
	return fmt.Sprintf("$%.2f", c)
}

func humanizeInt(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
