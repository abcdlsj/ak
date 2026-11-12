package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
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
				out.Empty(os.Stdout, "No providers yet.", "Add one by running `ak`.")
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

			printStatus(cfg, names, health, quotas, used)
			return nil
		},
	}
}

// printStatus writes the table, then what to do about providers no balance
// source serves.
func printStatus(cfg *config.Config, names []string, health map[string]core.Status,
	quotas map[string]quota.Quota, used map[string]usage.ProviderRow) {
	cells := make([][]string, 0, len(names))
	var noSource []string
	for _, n := range names {
		q := quotas[n]
		if q.NoSource {
			noSource = append(noSource, n)
		}
		u := used[n]
		cells = append(cells, []string{
			n,
			string(cfg.Providers[n].Kind),
			healthText(health[n]),
			planCell(q),
			humanizeInt(u.Tokens),
			costStr(u.Cost),
		})
	}
	head := []string{"provider", "engine", "health", "plan",
		fmt.Sprintf("%dd tokens", statusDays), fmt.Sprintf("%dd cost", statusDays)}
	out.Table(os.Stdout, head, cells)
	if len(noSource) > 0 {
		fmt.Println()
		out.Hint(os.Stdout, "no balance source: %s (relays and gateways usually have none)", strings.Join(noSource, ", "))
	}
}

// healthText is a provider's local health: a mark and what is wrong, or just
// ok.
func healthText(st core.Status) string {
	if st.OK() {
		return out.Mark(out.Fine) + " ok"
	}
	return out.Mark(out.Broken) + " " + strings.Join(st.Problems(), ", ")
}

// planCell is a provider's balance and windows, a dim dash when no source
// serves it, or why the read failed.
func planCell(q quota.Quota) string {
	switch {
	case q.NoSource:
		return out.Or("")
	case q.Error != "":
		return out.Err("✗ ") + out.Reason(q.Error)
	}
	return out.Or(planText(q))
}
