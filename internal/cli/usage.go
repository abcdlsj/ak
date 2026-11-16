package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/spf13/cobra"
)

func newUsageCmd() *cobra.Command {
	var (
		asJSON   bool
		days     int
		since    string
		akOnly   bool
		noRemote bool
		renames  []string
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
  ak usage --rename main=dpsk   relabel what a route used to be called

The first scan parses the whole history and takes a few seconds; later runs
are incremental and near-instant. Cost is estimated from models.dev pricing;
models with no pricing contribute tokens only, never a dollar figure.

A provider on a remote account asks a service for a name the service chose,
which may be a route over several models rather than a model. The service is
asked what it actually served, so those tokens are priced and attributed by
model; --no-remote keeps ak offline and reports the route name as logged.`,
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
			for _, r := range renames {
				from, to, ok := strings.Cut(r, "=")
				if !ok || from == "" || to == "" {
					return fmt.Errorf("--rename wants old=new, got %q", r)
				}
				usage.Rename(rows, from, to)
			}
			if !noRemote {
				rows = refineRemote(cmd, cfg, rows, days)
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
	cmd.Flags().BoolVar(&noRemote, "no-remote", false, "Do not ask remote services what they served; report their route names as logged")
	cmd.Flags().StringArrayVar(&renames, "rename", nil, "Relabel history logged under an old name: old=new (repeatable)")
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

// usageModelsShown is how many models the model table shows.
const usageModelsShown = 10

// refineRemote asks each remote service what it served and replaces the route
// name the logs recorded with the models behind it. A service that cannot be
// asked is said so: its tokens stay attributed to a name no price matches.
func refineRemote(cmd *cobra.Command, cfg *config.Config, rows []usage.Row, days int) []usage.Row {
	if days <= 0 {
		// The whole history: ask for as much as a service keeps.
		days = 0
	}
	served, errs := usage.FetchRemoteUsage(cmd.Context(), cfg, days)
	for _, err := range errs {
		warnf("ak: usage of %v; its tokens are reported under the route name", err)
	}
	return usage.Refine(rows, served, cfg)
}

// printUsage prints a summary: the total, by provider, by model, and the span
// of the history it covers.
func printUsage(s usage.Summary, cfg *config.Config) {
	printUsageTotal(s)

	// ak's own providers first; engine logins and ids from other tools
	// (cc-switch, a base codex config) are listed apart.
	own, other := splitUsage(s.ByProvider, cfg)
	if len(own) > 0 {
		printUsageHead("By provider", "")
		printUsageProviders(own)
	}
	if len(other) > 0 {
		printUsageHead("Outside ak", "engine logins and other tools, not run through ak")
		printUsageProviders(other)
	}
	if len(s.ByModel) > 0 {
		printUsageHead("By model", "")
		printUsageModels(s.ByModel)
	}
	if len(s.ByDate) > 0 {
		fmt.Println()
		out.Hint(os.Stdout, "%d days on record, earliest %s, latest %s",
			len(s.ByDate), s.ByDate[0].Date, s.ByDate[len(s.ByDate)-1].Date)
	}
}

// printUsageTotal is the summary's first line, and what the cost leaves out.
func printUsageTotal(s usage.Summary) {
	line := humanizeInt(s.TotalTokens) + " tokens"
	if s.TotalCost > 0 {
		line += fmt.Sprintf(" · ≈ $%.2f", s.TotalCost)
	}
	fmt.Println(out.Bold(line))
	if s.UnpricedTokens > 0 {
		out.Hint(os.Stdout, "%s tokens have no pricing data and are excluded from cost", humanizeInt(s.UnpricedTokens))
	}
}

// printUsageHead starts a section: its title, and a note under it when one is
// wanted.
func printUsageHead(title, note string) {
	fmt.Println()
	fmt.Println(out.Bold(title))
	if note != "" {
		out.Hint(os.Stdout, "%s", note)
	}
}

// printUsageProviders is a table of providers by tokens.
func printUsageProviders(rows []usage.ProviderRow) {
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{r.Name, humanizeInt(r.Tokens), costCell(r.Cost)}
	}
	out.Table(os.Stdout, []string{"provider", "tokens", "cost"}, cells)
}

// printUsageModels is the top models by tokens, and how many are left out.
func printUsageModels(rows []usage.ModelRow) {
	shown := rows[:min(len(rows), usageModelsShown)]
	cells := make([][]string, len(shown))
	for i, r := range shown {
		cells[i] = []string{r.Model, humanizeInt(r.Tokens), costCell(r.Cost)}
	}
	out.Table(os.Stdout, []string{"model", "tokens", "cost"}, cells)
	if rest := len(rows) - len(shown); rest > 0 {
		out.Hint(os.Stdout, "… %d more", rest)
	}
}

// splitUsage splits the provider rows into ak's own and the others'.
func splitUsage(rows []usage.ProviderRow, cfg *config.Config) (own, other []usage.ProviderRow) {
	for _, r := range rows {
		if _, ok := cfg.Providers[r.Name]; ok {
			own = append(own, r)
		} else {
			other = append(other, r)
		}
	}
	return own, other
}

// costCell is a table's cost: a dollar figure, or a dim dash.
func costCell(c float64) string {
	if c <= 0 {
		return out.Or("")
	}
	return costStr(c)
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
