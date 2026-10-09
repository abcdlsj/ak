package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/spf13/cobra"
)

func newUsageCmd() *cobra.Command {
	var (
		asJSON bool
		days   int
		since  string
		akOnly bool
	)
	cmd := &cobra.Command{
		Use:   "usage [name]",
		Short: "Report token usage across claude, codex and pi",
		Long: `Aggregate usage by parsing the local session logs.

  ak usage                 all history
  ak usage --days 7        the last seven days, today included
  ak usage --since 2026-10-01
  ak usage bilicodex       one provider
  ak usage --ak-only       only providers in providers.toml

The first scan parses the whole history and takes a few seconds; later runs
are incremental and near-instant. Cost is estimated from models.dev pricing;
models with no pricing contribute tokens only, never a dollar figure.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeProviders,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			f, err := usageFilter(days, since, args)
			if err != nil {
				return err
			}
			rows, err := usage.Load(cfg)
			if err != nil {
				return err
			}
			if akOnly {
				kept := rows[:0]
				for _, r := range rows {
					if _, ok := cfg.Providers[r.Provider]; ok {
						kept = append(kept, r)
					}
				}
				rows = kept
			}
			sum := usage.Summarize(rows, cfg, f)
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(sum)
			}
			printUsage(sum, cfg)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&days, "days", 0, "Only the last N days, today included")
	cmd.Flags().StringVar(&since, "since", "", "Only from this date on, YYYY-MM-DD")
	cmd.Flags().BoolVar(&akOnly, "ak-only", false, "Only providers in providers.toml, not engine logins or other tools' ids")
	cmd.MarkFlagsMutuallyExclusive("days", "since")
	return cmd
}

// usageFilter turns the flags and the optional provider argument into a filter.
func usageFilter(days int, since string, args []string) (usage.Filter, error) {
	var f usage.Filter
	if days < 0 {
		return f, fmt.Errorf("--days must be positive")
	}
	f.Since = usage.SinceDays(days)
	if since != "" {
		if _, err := time.Parse("2006-01-02", since); err != nil {
			return f, fmt.Errorf("--since wants YYYY-MM-DD, got %q", since)
		}
		f.Since = since
	}
	if len(args) > 0 {
		f.Provider = args[0]
	}
	return f, nil
}

func printUsage(s usage.Summary, cfg *config.Config) {
	fmt.Printf("total %s tokens", humanizeInt(s.TotalTokens))
	if s.TotalCost > 0 {
		fmt.Printf("  ·  about $%.2f", s.TotalCost)
	}
	if s.UnpricedTokens > 0 {
		fmt.Printf("  (%s tokens have no pricing data and are excluded from cost)", humanizeInt(s.UnpricedTokens))
	}
	fmt.Println()

	// ak's own providers first; engine logins and ids from other tools
	// (cc-switch, a base codex config) are listed apart.
	var own, other []usage.ProviderRow
	for _, r := range s.ByProvider {
		if _, ok := cfg.Providers[r.Name]; ok {
			own = append(own, r)
		} else {
			other = append(other, r)
		}
	}
	printProviderRows("by provider", own)
	printProviderRows("not in ak (engine logins, other tools)", other)

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

func printProviderRows(title string, rows []usage.ProviderRow) {
	if len(rows) == 0 {
		return
	}
	fmt.Println(title)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  provider\ttokens\tcost")
	for _, r := range rows {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Name, humanizeInt(r.Tokens), costStr(r.Cost))
	}
	w.Flush()
	fmt.Println()
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
