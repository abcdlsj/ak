package cli

import (
	"context"
	"encoding/json"
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
	var asJSON bool
	cmd := &cobra.Command{
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
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode([]statusRow{})
				}
				out.Empty(os.Stdout, "No providers yet.", "Add one by running `ak`.")
				return nil
			}

			health := core.Statuses(cfg)
			quotas, err := providerQuotas(cmd.Context(), cfg, names)
			if err != nil {
				return err
			}
			used := map[string]usage.ProviderRow{}
			if rows, err := usage.Load(cfg); err != nil {
				warnf("ak: usage: %v", err)
			} else {
				// A remote's own report names the models behind a route, so
				// its tokens carry a price rather than a name nothing matches.
				if served, errs := usage.FetchRemoteUsage(cmd.Context(), cfg, statusDays); len(errs) == 0 {
					rows = usage.Refine(rows, served, cfg)
				}
				sum := usage.Summarize(rows, cfg, usage.Filter{Since: usage.SinceDays(statusDays)})
				for _, r := range sum.ByProvider {
					used[r.Name] = r
				}
			}
			agyUsed := map[string]usage.AgyActivity{}
			for _, a := range usage.AgyActivities(cfg, usage.Filter{Since: usage.SinceDays(statusDays)}) {
				agyUsed[a.Provider] = a
			}

			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(buildStatusRows(cfg, names, health, quotas, used, agyUsed))
			}
			printStatus(cfg, names, health, quotas, used, agyUsed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

type statusRow struct {
	Provider         string       `json:"provider"`
	Engine           string       `json:"engine"`
	OK               bool         `json:"ok"`
	Problems         []string     `json:"problems,omitempty"`
	Quota            *quota.Quota `json:"quota,omitempty"`
	Tokens           int64        `json:"tokens"`
	Cost             float64      `json:"cost,omitempty"`
	AgyConversations int          `json:"agy_conversations,omitempty"`
	AgySteps         int64        `json:"agy_steps,omitempty"`
}

func buildStatusRows(cfg *config.Config, names []string, health map[string]core.Status,
	quotas map[string]quota.Quota, used map[string]usage.ProviderRow, agyUsed map[string]usage.AgyActivity) []statusRow {
	rows := make([]statusRow, 0, len(names))
	for _, n := range names {
		p := cfg.Providers[n]
		st := health[n]
		u := used[n]
		r := statusRow{
			Provider: n,
			Engine:   string(p.Kind),
			OK:       st.OK(),
			Problems: st.Problems(),
			Tokens:   u.Tokens,
			Cost:     u.Cost,
		}
		if q, ok := quotas[n]; ok && !q.NoSource {
			qc := q
			r.Quota = &qc
		}
		if a, ok := agyUsed[n]; ok && a.Conversations > 0 {
			r.AgyConversations = a.Conversations
			r.AgySteps = a.Steps
		}
		rows = append(rows, r)
	}
	return rows
}

// providerQuotas reads each holder among names once and gives every
// provider its holder's reading: a provider on an account, the account's.
// A pool has no balance of its own and gets none.
func providerQuotas(ctx context.Context, cfg *config.Config, names []string) (map[string]quota.Quota, error) {
	var leaves []string
	for _, n := range names {
		if !cfg.Providers[n].IsPool() {
			leaves = append(leaves, n)
		}
	}
	if len(leaves) == 0 {
		return map[string]quota.Quota{}, nil
	}
	holders, err := core.Holders(cfg, leaves)
	if err != nil {
		return nil, err
	}
	byHolder := map[string]quota.Quota{}
	for i, q := range queryQuotas(ctx, holders) {
		byHolder[holderKey(holders[i].Account, holders[i].Name)] = q
	}
	out := map[string]quota.Quota{}
	for _, n := range leaves {
		if a := cfg.Providers[n].Account; a != "" {
			out[n] = byHolder[holderKey(true, a)]
		} else {
			out[n] = byHolder[holderKey(false, n)]
		}
	}
	return out, nil
}

// holderKey keeps an account and a provider of the same name apart.
func holderKey(account bool, name string) string {
	if account {
		return "account:" + name
	}
	return "provider:" + name
}

// printStatus writes the table, then what to do about providers no balance
// source serves.
func printStatus(cfg *config.Config, names []string, health map[string]core.Status,
	quotas map[string]quota.Quota, used map[string]usage.ProviderRow, agyUsed map[string]usage.AgyActivity) {
	cells := make([][]string, 0, len(names))
	var noSource []string
	for _, n := range names {
		p := cfg.Providers[n]
		q := quotas[n]
		// A provider that signs in through the engine itself has no balance API
		// for ak to query, so listing it as missing a source says nothing the
		// user can act on.
		if q.NoSource && !p.UsesEngineLogin() {
			noSource = append(noSource, n)
		}
		u := used[n]
		usageCol := humanizeInt(u.Tokens)
		costCol := costStr(u.Cost)
		if p.Kind == config.KindAntigravity {
			if a, ok := agyUsed[n]; ok && a.Conversations > 0 {
				usageCol = fmt.Sprintf("%d conv · %s steps", a.Conversations, humanizeInt(a.Steps))
			} else {
				usageCol = out.Or("")
			}
			costCol = out.Or("")
		}
		cells = append(cells, []string{
			n,
			string(p.Kind),
			healthText(health[n]),
			planCell(q),
			usageCol,
			costCol,
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
