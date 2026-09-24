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
		Short: "Report token usage across claude and codex",
		Long: `Aggregate usage by parsing the local session logs.

The first scan parses the whole history and takes a few seconds; later runs
are incremental and near-instant. Cost is estimated from models.dev pricing;
models with no pricing contribute tokens only, never a dollar figure.`,
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func printUsage(s usage.Summary) {
	fmt.Printf("total %s tokens", humanizeInt(s.TotalTokens))
	if s.TotalCost > 0 {
		fmt.Printf("  ·  about $%.2f", s.TotalCost)
	}
	if s.UnpricedTokens > 0 {
		fmt.Printf("  (%s tokens have no pricing data and are excluded from cost)", humanizeInt(s.UnpricedTokens))
	}
	fmt.Println()

	if len(s.ByProvider) > 0 {
		fmt.Println("by provider")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  provider\ttokens\tcost")
		for _, r := range s.ByProvider {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Name, humanizeInt(r.Tokens), costStr(r.Cost))
		}
		w.Flush()
		fmt.Println()
	}

	if len(s.ByModel) > 0 {
		fmt.Println("by model")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  model\ttokens\tcost")
		for _, r := range s.ByModel {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Model, humanizeInt(r.Tokens), costStr(r.Cost))
		}
		w.Flush()
		fmt.Println()
	}

	if len(s.ByDate) > 0 {
		fmt.Printf("%d days on record, earliest %s, latest %s.\n",
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
