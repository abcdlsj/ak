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
		bySess   bool
		sessID   string
	)
	cmd := &cobra.Command{
		Use:   "usage [name]",
		Short: "Report token usage across claude, codex, pi, and what agy recorded",
		Long: `Aggregate usage by parsing the local session logs.

  ak usage                 all history
  ak usage --days 7        the last seven days, today included
  ak usage --since 2026-10-01
  ak usage bilicodex       one provider
  ak usage --ak-only       only providers in providers.toml
  ak usage --rename main=dpsk   relabel what a route used to be called
  ak usage --sessions      each session's tokens and prompt-cache hit rate
  ak usage --session a1b2c3d4   one session, with the token breakdown

The first scan parses the whole history and takes a few seconds; later runs
are incremental and near-instant. Cost is estimated from models.dev pricing;
models with no pricing contribute tokens only, never a dollar figure.

Antigravity is counted apart: agy records no token usage at all, so what it
leaves behind is a conversation and its steps, and those are what ak reports.
No agy row ever shows a cache hit, because agy records no cache figures either.

A cache hit is cache reads over the whole prompt (input, cache reads, cache
writes), the one split claude, codex and pi all report.

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
			if bySess || sessID != "" {
				list, err := usage.Sessions(cfg)
				if err != nil {
					return err
				}
				list = filterSessions(list, cfg, f, sessID, akOnly)
				if asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(struct {
						Sessions []usage.Session `json:"sessions"`
					}{list})
				}
				printUsageSessions(list, sessID != "")
				return nil
			}
			sum := usage.Summarize(rows, cfg, f)
			agy := usage.AgyActivities(cfg, f)
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					usage.Summary
					Agy []usage.AgyActivity `json:"agy,omitempty"`
				}{sum, agy})
			}
			printUsage(sum, cfg, agy)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&days, "days", 0, "Only the last N days, today included")
	cmd.Flags().StringVar(&since, "since", "", "Only from this date on, YYYY-MM-DD")
	cmd.Flags().BoolVar(&akOnly, "ak-only", false, "Only providers in providers.toml, not engine logins or other tools' ids")
	cmd.Flags().BoolVar(&noRemote, "no-remote", false, "Do not ask remote services what they served; report their route names as logged")
	cmd.Flags().StringArrayVar(&renames, "rename", nil, "Relabel history logged under an old name: old=new (repeatable)")
	cmd.Flags().BoolVar(&bySess, "sessions", false, "List each session's tokens and prompt-cache hit rate")
	cmd.Flags().StringVar(&sessID, "session", "", "One session, by id or id prefix")
	cmd.MarkFlagsMutuallyExclusive("days", "since")
	cmd.MarkFlagsMutuallyExclusive("sessions", "session")
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
func printUsage(s usage.Summary, cfg *config.Config, agy []usage.AgyActivity) {
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
	if len(agy) > 0 {
		printUsageAgy(agy)
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

// usageSessionsShown is how many sessions the list shows before it refers you
// to --session for the rest.
const usageSessionsShown = 20

// filterSessions narrows the list by the same filters the aggregate uses, plus
// the id prefix --session takes.
func filterSessions(list []usage.Session, cfg *config.Config, f usage.Filter, id string, akOnly bool) []usage.Session {
	out := make([]usage.Session, 0, len(list))
	for _, s := range list {
		if f.Since != "" && s.Last.Local().Format("2006-01-02") < f.Since {
			continue
		}
		if f.Provider != "" && s.Provider != f.Provider {
			continue
		}
		if akOnly {
			if _, ok := cfg.Providers[s.Provider]; !ok {
				continue
			}
		}
		if id != "" && !strings.HasPrefix(s.ID, id) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// printUsageSessions prints one row per session. With one session asked for,
// the row is followed by the token breakdown a single session can carry, and
// nothing is left out.
func printUsageSessions(list []usage.Session, one bool) {
	if len(list) == 0 {
		out.Empty(os.Stdout, "No session matches.", "ak usage --sessions lists the ones on record.")
		return
	}
	printUsageHead("Sessions", "cache hit is cache reads over the whole prompt: input, cache reads and cache writes")
	shown := list
	if len(shown) > usageSessionsShown {
		shown = shown[:usageSessionsShown]
	}
	cells := make([][]string, len(shown))
	for i, s := range shown {
		cells[i] = []string{s.Engine, s.Provider, shortSession(s.ID), humanizeInt(int64(s.Requests)), hitCell(s), humanizeInt(s.Tokens.Total()), sessionLast(s.Last)}
	}
	out.Table(os.Stdout, []string{"engine", "provider", "session", "requests", "cache hit", "tokens", "last used"}, cells)
	if one && len(list) == 1 {
		t := shown[0].Tokens
		out.Hint(os.Stdout, "input %s · output %s · cache read %s · cache write %s",
			humanizeInt(t.Input), humanizeInt(t.Output), humanizeInt(t.CacheRead), humanizeInt(t.CacheWrite))
	} else if one {
		out.Hint(os.Stdout, "%d sessions match that prefix", len(list))
	} else if rest := len(list) - len(shown); rest > 0 {
		out.Hint(os.Stdout, "… %d more; --session <id> shows one in full", rest)
	}
	out.Hint(os.Stdout, "%s", usage.CacheNote)
}

// hitCell is a session's prompt-cache hit rate, or a dim dash when the session
// reported no cache activity at all.
func hitCell(s usage.Session) string {
	h, ok := s.HitRate()
	if !ok {
		return out.Or("")
	}
	return fmt.Sprintf("%.1f%%", h)
}

// shortSession is a session id as much of it as a table needs; --session takes
// any prefix, so the rest is only ever typed.
func shortSession(id string) string {
	if id == "" {
		return out.Or("")
	}
	return id[:min(len(id), 8)]
}

// sessionLast is a session's last request, or a dim dash when undated.
func sessionLast(t time.Time) string {
	if t.IsZero() {
		return out.Or("")
	}
	return t.Local().Format("2006-01-02 15:04")
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

// printUsageAgy prints what the antigravity providers have to say, which is
// conversations and steps: agy keeps no token counts, so no row here has a
// token or a cost figure, and saying so is the honest report.
func printUsageAgy(rows []usage.AgyActivity) {
	printUsageHead("Antigravity", "agy records conversations and their steps, not tokens: nothing here is priced")
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{r.Provider, humanizeInt(int64(r.Conversations)), humanizeInt(r.Steps), agyLastCell(r.Last)}
	}
	out.Table(os.Stdout, []string{"provider", "conversations", "steps", "last used"}, cells)
}

// agyLastCell is when a provider was last used, or a dim dash when nothing is
// recorded.
func agyLastCell(t time.Time) string {
	if t.IsZero() {
		return out.Or("")
	}
	return t.Local().Format("2006-01-02 15:04")
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
