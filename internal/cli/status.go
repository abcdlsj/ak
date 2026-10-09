package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/spf13/cobra"
)

// statusDays is the usage window ak status reports.
const statusDays = 7

// newStatusCmd answers "are my providers all right" on one screen: local
// health, balance and recent usage. It sends no model request; `ak check`
// does that.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status [name...]",
		ValidArgsFunction: completeProviders,
		Short:             "Show each provider's health, balance and recent usage",
		Long: `Show each provider's health, balance and usage of the last 7 days.

Health is what ak can tell locally (key, engine, command); balance comes from
the vendor's balance API, as ak quota; usage from the local session logs, as
ak usage. No model request is sent: ak check does that.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				names = cfg.Names()
			}
			for _, n := range names {
				if _, ok := cfg.Providers[n]; !ok {
					return fmt.Errorf("provider %q does not exist", n)
				}
			}
			if len(names) == 0 {
				fmt.Println("No providers yet. Run `ak` to add one.")
				return nil
			}

			health := core.Statuses(cfg)
			// A pool has no balance of its own; its members are asked when listed.
			var leaves []string
			for _, n := range names {
				if !cfg.Providers[n].IsPool() {
					leaves = append(leaves, n)
				}
			}
			quotas := map[string]quota.Quota{}
			for _, q := range queryQuotas(cmd.Context(), cfg, leaves) {
				quotas[q.Provider] = q
			}
			used := map[string]usage.ProviderRow{}
			if rows, err := usage.Load(cfg); err != nil {
				warnf("ak: usage: %v", err)
			} else {
				sum := usage.Summarize(rows, cfg, usage.Filter{Since: usage.SinceDays(statusDays)})
				for _, r := range sum.ByProvider {
					used[r.Name] = r
				}
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "provider\tengine\thealth\tbalance\tplan\t%dd tokens\t%dd cost\n", statusDays, statusDays)
			for _, n := range names {
				p := cfg.Providers[n]
				h := "ok"
				if st := health[n]; !st.OK() {
					h = "✗ " + strings.Join(st.Problems(), ", ")
				}
				bal, plan := "-", ""
				if q, ok := quotas[n]; ok {
					switch {
					case q.NoSource:
					case q.Error != "":
						bal = "✗"
					default:
						bal, plan = balanceText(q), usageText(q)
					}
				}
				u := used[n]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n, p.Kind, h, bal, plan, humanizeInt(u.Tokens), costStr(u.Cost))
			}
			w.Flush()
			return nil
		},
	}
}
