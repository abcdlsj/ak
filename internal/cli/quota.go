package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/spf13/cobra"
)

func newQuotaCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "quota [account|provider...]",
		ValidArgsFunction: completeProviders,
		Short:             "Query the balance and plan of each account",
		Long: `Ask what is left: once per account, which every provider on it shares,
and once per provider with an endpoint of its own. A provider named is asked
through its account; a pool through its members.

The source is the account vendor's, else detected from the endpoint host
(deepseek, kimi, zhipu, commandcode, opencode-go, …), else named with quota.
~/.config/ak/quota.d adds sources: a TOML spec for one request, or any
executable (a script, a Go binary) that prints the quota as JSON; see
docs/status.md. quota_cmd runs a command for one provider.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			holders, err := core.Holders(cfg, args)
			if err != nil {
				return err
			}
			if len(holders) == 0 {
				out.Empty(os.Stdout, "Nothing to query.", "Sign in with `ak login <vendor>`, or add a provider with `ak add`.")
				return nil
			}
			for _, err := range quota.PluginErrors() {
				fmt.Fprintln(os.Stderr, "warning: quota plugin skipped:", err)
			}
			results := queryQuotas(cmd.Context(), holders)
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(results)
			}
			printQuota(holders, results)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.AddCommand(newQuotaWaitCmd(), newQuotaBoardCmd())
	return cmd
}

func newQuotaBoardCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "board",
		Aliases: []string{"ui"},
		Short:   "Every plan and balance as cards, in a full-screen view",
		Long: `Show each account, and each provider on its own with a balance source, as a
card: every allowance window with the share used as a big pixel number, a
tick bar and when it resets, and money left. r reads them again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return ui.RunBoard(cfg)
		},
	}
}

func newQuotaWaitCmd() *cobra.Command {
	var (
		every   time.Duration
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <account|provider>",
		Short: "Block until an account or provider has allowance again",
		Long: `Return as soon as it has allowance: no plan window used up and no balance
at zero. A provider waits on its account; a pool, on any member's.

  ak quota wait kimi && ak-kimi -p "..."
  ak quota wait pool --timeout 6h

While it waits it reads the quota again at the reset time the vendor gave, or
every --every when there is none, and says when on stderr. It exits 1 on
--timeout, and at once when the provider has no balance source.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFirstProvider,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			holders, err := core.Holders(cfg, args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			return waitQuota(ctx, holders, every, askQuota)
		},
	}
	cmd.Flags().DurationVar(&every, "every", time.Minute, "How often to read the quota when no reset time is known")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Give up after this long (0 waits indefinitely)")
	return cmd
}

// quotaPollMax bounds one sleep, so a plan that resets early is noticed.
const quotaPollMax = 10 * time.Minute

// waitQuota polls the holders' quota until one has allowance. One that cannot
// be read at all (no source) is an error rather than a wait.
func waitQuota(ctx context.Context, holders []core.Holder, every time.Duration,
	ask func(context.Context, core.Holder) quota.Quota) error {
	if every <= 0 {
		every = time.Minute
	}
	for {
		var soonest *time.Time
		var reasons []string
		readable := 0
		for _, h := range holders {
			n := h.Name
			q := ask(ctx, h)
			if q.NoSource {
				reasons = append(reasons, n+": no balance source")
				continue
			}
			readable++
			if q.Error != "" {
				reasons = append(reasons, n+": "+q.Error)
				continue
			}
			spent, back := q.Spent()
			if !spent {
				fmt.Fprintf(os.Stderr, "%s has allowance\n", n)
				return nil
			}
			if back != nil && (soonest == nil || back.Before(*soonest)) {
				soonest = back
			}
			reasons = append(reasons, n+": spent")
		}
		if readable == 0 {
			return fmt.Errorf("cannot read the quota of %s; set quota or quota_cmd", strings.Join(holderNames(holders), ", "))
		}
		wait := every
		if soonest != nil {
			// A few seconds past the reset, so the vendor has rolled over.
			wait = max(time.Until(*soonest)+5*time.Second, time.Second)
		}
		wait = min(wait, quotaPollMax)
		fmt.Fprintf(os.Stderr, "%s; checking again at %s\n", strings.Join(reasons, "; "), time.Now().Add(wait).Format("15:04:05"))
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("still no allowance at --timeout")
			}
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// askQuota reads one holder's balance.
func askQuota(ctx context.Context, h core.Holder) quota.Quota {
	key, err := secrets.Default().Resolve(h.Provider)
	if err != nil {
		return quota.Quota{Provider: h.Name, Error: err.Error()}
	}
	return quota.Query(ctx, h.Name, h.Provider, key)
}

// queryQuotas reads every holder's balance in parallel: one slow vendor
// should not hold up the rest.
func queryQuotas(ctx context.Context, holders []core.Holder) []quota.Quota {
	results := make([]quota.Quota, len(holders))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, h := range holders {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = askQuota(ctx, h)
		}()
	}
	wg.Wait()
	return results
}

func holderNames(hs []core.Holder) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Name
	}
	return out
}

// printQuota writes one row per holder, then what to do about those no
// balance source serves: a gap in coverage, counted once rather than on
// every row.
func printQuota(holders []core.Holder, results []quota.Quota) {
	cells := make([][]string, 0, len(results))
	var noSource []string
	for i, q := range results {
		if q.NoSource && !holders[i].Provider.UsesEngineLogin() {
			noSource = append(noSource, q.Provider)
		}
		cells = append(cells, append([]string{holderCell(holders[i])}, quotaRow(q)...))
		// A remote ak service's own accounts, under it.
		for _, a := range q.Accounts {
			cells = append(cells, append([]string{"  " + a.Provider}, quotaRow(a)...))
		}
	}
	out.Table(os.Stdout, []string{"account / provider", "source", "balance", "plan", "note"}, cells)
	if len(noSource) == 0 {
		return
	}
	fmt.Println()
	out.Hint(os.Stdout, "no balance source: %s (relays and gateways usually have none)", strings.Join(noSource, ", "))
	out.Hint(os.Stdout, "give its vendor a quota source, or add one to ~/.config/ak/quota.d: a TOML spec or any executable printing JSON")
}

// holderCell names a holder: an account with the providers on it, or a
// provider of its own.
func holderCell(h core.Holder) string {
	if !h.Account {
		return h.Name
	}
	if len(h.Users) == 0 {
		return h.Name + out.Dim(" · no providers")
	}
	return h.Name + out.Dim(" · "+strings.Join(h.Users, ", "))
}

// quotaRow is one balance reading, without whose it is; a failed read says
// why in the note, one with no source says nothing.
func quotaRow(q quota.Quota) []string {
	switch {
	case q.NoSource:
		return []string{out.Or(""), out.Or(""), out.Or(""), out.Or("")}
	case q.Error != "":
		return []string{out.Or(q.Source), out.Or(""), out.Or(""),
			out.Mark(out.Broken) + " " + out.Reason(q.Error)}
	}
	return []string{out.Or(q.Source), out.Or(balanceText(q)), out.Or(usageText(q)), out.Or(q.Detail)}
}

// balanceText is a quota's money left, empty when the source reports none.
func balanceText(q quota.Quota) string {
	if q.Balance == nil {
		return ""
	}
	if q.Currency == "" {
		return amount(*q.Balance)
	}
	return amount(*q.Balance) + " " + q.Currency
}

// amount prints money to two decimals, without trailing zeros: 15.986 is
// 15.99 and 15.00 is 15.
func amount(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// percent prints a share to at most one decimal.
func percent(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + "%"
}

// usageText is what a plan has used: money against a limit, and its windows.
func usageText(q quota.Quota) string {
	parts := windowParts(q)
	switch {
	case q.Used != nil && q.Limit != nil:
		parts = append([]string{amount(*q.Used) + "/" + amount(*q.Limit)}, parts...)
	case q.Used != nil:
		parts = append([]string{amount(*q.Used) + " used"}, parts...)
	}
	return joinParts(parts)
}

// planText is a balance and its plan windows on one line, as ak status shows
// them.
func planText(q quota.Quota) string {
	parts := windowParts(q)
	if b := balanceText(q); b != "" {
		parts = append([]string{b}, parts...)
	}
	return joinParts(parts)
}

// windowParts is each plan window: its share used and when it resets.
func windowParts(q quota.Quota) []string {
	parts := make([]string, 0, len(q.Windows))
	for _, win := range q.Windows {
		s := win.Name + " " + percent(win.Used)
		if win.Resets != nil {
			s += " " + resetText(*win.Resets)
		}
		parts = append(parts, s)
	}
	return parts
}

// resetText is when a plan window resets: the time alone when that is today,
// the day too otherwise, in local time as the TUI shows it.
func resetText(t time.Time) string {
	t = t.Local()
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "↻ " + t.Format("15:04")
	}
	return "↻ " + t.Format("01-02 15:04")
}

func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}
