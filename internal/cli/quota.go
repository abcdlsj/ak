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
	"text/tabwriter"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

func newQuotaCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "quota [name...]",
		ValidArgsFunction: completeProviders,
		Short:             "Query provider balance and plan usage",
		Long: `Ask each provider's balance API what is left.

The source is detected from the endpoint host (deepseek, openrouter, moonshot,
siliconflow, stepfun, kimi, zhipu, minimax), or named with the provider's quota
field (newapi is never detected). TOML plugins in ~/.config/ak/quota.d add or
override sources. A provider with quota_cmd runs that script instead. Given no
names, every provider is asked. A pool is asked through its members.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names, err := quotaTargets(cfg, args)
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Println("No providers to query.")
				return nil
			}
			for _, err := range quota.PluginErrors() {
				fmt.Fprintln(os.Stderr, "warning: quota plugin skipped:", err)
			}
			resolver := secrets.Default()
			results := make([]quota.Quota, len(names))
			// Providers on the same account (same key, host and source) get
			// the same answer, so each account is asked once.
			type job struct {
				name    string
				p       config.Provider
				key     string
				targets []int
			}
			var jobs []*job
			byKey := map[string]*job{}
			for i, name := range names {
				p := cfg.Providers[name]
				key, err := resolver.Resolve(p)
				if err != nil {
					results[i] = quota.Quota{Provider: name, Error: err.Error()}
					continue
				}
				k := quota.DedupKey(p, key)
				if j := byKey[k]; j != nil {
					j.targets = append(j.targets, i)
					continue
				}
				j := &job{name: name, p: p, key: key, targets: []int{i}}
				byKey[k] = j
				jobs = append(jobs, j)
			}
			// Ask in parallel: one slow vendor should not hold up the rest.
			const parallel = 8
			sem := make(chan struct{}, parallel)
			var wg sync.WaitGroup
			for _, j := range jobs {
				wg.Add(1)
				sem <- struct{}{}
				go func(j *job) {
					defer wg.Done()
					defer func() { <-sem }()
					q := quota.Query(cmd.Context(), j.name, j.p, j.key)
					for _, i := range j.targets {
						q.Provider = names[i]
						results[i] = q
					}
				}(j)
			}
			wg.Wait()
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(results)
			}
			printQuota(results)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.AddCommand(newQuotaWaitCmd())
	return cmd
}

func newQuotaWaitCmd() *cobra.Command {
	var (
		every   time.Duration
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <name>",
		Short: "Block until a provider has allowance again",
		Long: `Return as soon as the provider has allowance: no plan window used up and
no balance at zero. For a pool, as soon as any member does.

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
			names, err := quotaTargets(cfg, args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			return waitQuota(ctx, cfg, names, every, func(ctx context.Context, name string, p config.Provider) quota.Quota {
				key, err := secrets.Default().Resolve(p)
				if err != nil {
					return quota.Quota{Provider: name, Error: err.Error()}
				}
				return quota.Query(ctx, name, p, key)
			})
		},
	}
	cmd.Flags().DurationVar(&every, "every", time.Minute, "How often to read the quota when no reset time is known")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Give up after this long (0 waits indefinitely)")
	return cmd
}

// quotaPollMax bounds one sleep, so a plan that resets early is noticed.
const quotaPollMax = 10 * time.Minute

// waitQuota polls the providers' quota until one has allowance. A provider
// that cannot be read at all (no source) is an error rather than a wait.
func waitQuota(ctx context.Context, cfg *config.Config, names []string, every time.Duration,
	ask func(context.Context, string, config.Provider) quota.Quota) error {
	if every <= 0 {
		every = time.Minute
	}
	for {
		var soonest *time.Time
		var reasons []string
		readable := 0
		for _, n := range names {
			q := ask(ctx, n, cfg.Providers[n])
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
			return fmt.Errorf("cannot read the quota of %s; set quota or quota_cmd", strings.Join(names, ", "))
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

// quotaTargets expands the requested names into concrete providers: a pool
// becomes its members, and no provider is listed twice. With no names, every
// provider is included.
func quotaTargets(cfg *config.Config, args []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	expand := func(n string) error {
		p, ok := cfg.Providers[n]
		if !ok {
			return fmt.Errorf("provider %q does not exist", n)
		}
		if p.IsPool() {
			for _, m := range cfg.Leaves(n) {
				add(m)
			}
			return nil
		}
		add(n)
		return nil
	}
	if len(args) == 0 {
		for _, n := range cfg.Names() {
			if err := expand(n); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	for _, n := range args {
		if err := expand(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func printQuota(results []quota.Quota) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "provider\tsource\tbalance\tusage\tdetail")
	var noSource int
	for _, q := range results {
		switch {
		case q.NoSource:
			// Not a failure: say so once below rather than on every row.
			noSource++
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", q.Provider, "-", "-", "", "")
		case q.Error != "":
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", q.Provider, dash(q.Source), "✗", "", q.Error)
		default:
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				q.Provider, dash(q.Source), balanceText(q), usageText(q), q.Detail)
		}
	}
	w.Flush()
	if noSource > 0 {
		fmt.Printf("\n%d without a balance source: set quota = <%s>, or quota_cmd\n",
			noSource, strings.Join(quota.Sources(), "|"))
	}
}

func balanceText(q quota.Quota) string {
	if q.Balance == nil {
		return "-"
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

func usageText(q quota.Quota) string {
	var parts []string
	if q.Used != nil && q.Limit != nil {
		parts = append(parts, amount(*q.Used)+"/"+amount(*q.Limit))
	} else if q.Used != nil {
		parts = append(parts, amount(*q.Used)+" used")
	}
	for _, win := range q.Windows {
		s := win.Name + " " + percent(win.Used)
		if win.Resets != nil {
			s += " (resets " + win.Resets.Local().Format("01-02 15:04") + ")"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return joinParts(parts)
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
