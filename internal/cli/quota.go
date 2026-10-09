package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

func newQuotaCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "quota [name...]",
		Short: "Query provider balance and plan usage",
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
			// Ask in parallel: one slow vendor should not hold up the rest.
			const parallel = 8
			sem := make(chan struct{}, parallel)
			var wg sync.WaitGroup
			for i, name := range names {
				wg.Add(1)
				sem <- struct{}{}
				go func(i int, name string) {
					defer wg.Done()
					defer func() { <-sem }()
					p := cfg.Providers[name]
					key, err := resolver.Resolve(p)
					if err != nil {
						results[i] = quota.Quota{Provider: name, Error: err.Error()}
						return
					}
					results[i] = quota.Query(cmd.Context(), name, p, key)
				}(i, name)
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
	return cmd
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
			for _, m := range p.Members {
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
	for _, q := range results {
		if q.Error != "" {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", q.Provider, dash(q.Source), "✗", "", q.Error)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			q.Provider, dash(q.Source), balanceText(q), usageText(q), q.Detail)
	}
	w.Flush()
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
