package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/probe"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "check [name...]",
		ValidArgsFunction: completeProviders,
		Short:             "Send each provider a minimal real request",
		Long: `Send each provider one minimal, non-streamed request and report whether it
answers: ok, slow (over 6s), auth (key rejected), model (model not served) or
fail. Each check spends a few tokens.

Given no names, every provider is checked. A pool is checked through its
members, straight to their upstreams rather than through the gateway, and is
ok when any member is. Exits 1 when any provider is not ok or slow.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			results, err := runChecks(cmd.Context(), cfg, args, secrets.Default())
			if err != nil {
				return err
			}
			if len(results) == 0 {
				out.Empty(os.Stdout, "No providers to check.", "")
				return nil
			}
			if asJSON {
				if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
					return err
				}
			} else {
				printChecks(results)
			}
			if n := failedChecks(results); n > 0 {
				return fmt.Errorf("%d of %d providers failed the check", n, len(results))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

// checkRows lists the rows to show, in order: a pool's members come before the
// pool itself, and no provider is listed twice.
func checkRows(cfg *config.Config, args []string) ([]string, error) {
	names := args
	if len(names) == 0 {
		names = cfg.Names()
	}
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range names {
		p, ok := cfg.Providers[n]
		if !ok {
			return nil, fmt.Errorf("provider %q does not exist", n)
		}
		for _, m := range p.Members {
			if _, ok := cfg.Providers[m]; !ok {
				return nil, fmt.Errorf("pool %q names missing member %q", n, m)
			}
		}
		if p.IsPool() {
			for _, m := range cfg.Leaves(n) {
				add(m)
			}
		}
		add(n)
	}
	return out, nil
}

// runChecks checks every concrete provider in parallel, then folds each
// pool's members into the pool's row.
func runChecks(ctx context.Context, cfg *config.Config, args []string, resolver secrets.Resolver) ([]probe.Result, error) {
	rows, err := checkRows(cfg, args)
	if err != nil {
		return nil, err
	}
	results := make([]probe.Result, len(rows))
	// One slow vendor should not hold up the rest.
	const parallel = 8
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, name := range rows {
		p := cfg.Providers[name]
		if p.IsPool() {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, name string, p config.Provider) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = checkOne(ctx, name, p, resolver)
		}(i, name, p)
	}
	wg.Wait()

	byName := map[string]probe.Result{}
	for i, name := range rows {
		byName[name] = results[i]
	}
	for i, name := range rows {
		p := cfg.Providers[name]
		if !p.IsPool() {
			continue
		}
		leaves := cfg.Leaves(name)
		members := make([]probe.Result, 0, len(leaves))
		for _, m := range leaves {
			members = append(members, byName[m])
		}
		results[i] = probe.Aggregate(name, members)
	}
	return results, nil
}

func checkOne(ctx context.Context, name string, p config.Provider, resolver secrets.Resolver) probe.Result {
	key, err := resolver.Resolve(p)
	if err != nil {
		return probe.Result{Provider: name, Status: probe.StatusFail, Detail: "key: " + err.Error()}
	}
	r := probe.Check(ctx, p, key)
	r.Provider = name
	return r
}

func failedChecks(results []probe.Result) int {
	n := 0
	for _, r := range results {
		if !r.Status.Passed() {
			n++
		}
	}
	return n
}

// printChecks writes one row per provider: its mark, how fast it answered and
// what went wrong.
func printChecks(results []probe.Result) {
	cells := make([][]string, 0, len(results))
	for _, r := range results {
		latency := ""
		if r.LatencyMS > 0 {
			latency = fmt.Sprintf("%dms", r.LatencyMS)
		}
		cells = append(cells, []string{
			r.Provider,
			checkText(r.Status),
			out.Or(latency),
			out.Or(r.Model),
			out.Or(out.Reason(r.Detail)),
		})
	}
	out.Table(os.Stdout, []string{"provider", "status", "latency", "model", "note"}, cells)
}

// checkText is a check's mark and its status word.
func checkText(s probe.Status) string {
	return out.Mark(checkLevel(s)) + " " + string(s)
}

// checkLevel is the mark a status earns: ok is fine, slow is worth a look, a
// skip says nothing and anything else is broken.
func checkLevel(s probe.Status) out.Level {
	switch s {
	case probe.StatusOK:
		return out.Fine
	case probe.StatusSlow:
		return out.Notice
	case probe.StatusSkip:
		return out.Unknown
	}
	return out.Broken
}
