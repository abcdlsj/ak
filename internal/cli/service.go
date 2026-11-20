package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/spf13/cobra"
)

func newServeTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Issue, list and revoke the service's tokens",
	}
	var routes []string
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Issue a token, printed once",
		Long: `Issue a token for one machine and print it once; only its hash is kept.
--route limits it to routes and accounts (repeatable); without it the
token may use every one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Service.Tokens[name]; ok {
				return fmt.Errorf("token %q exists; revoke it first to issue it again", name)
			}
			tok, hash, err := gateway.NewToken()
			if err != nil {
				return err
			}
			err = core.ChangeService(cfg, func(s *config.Service) {
				s.Tokens[name] = config.Token{Hash: hash, Routes: routes, Created: time.Now().UTC().Truncate(time.Second)}
			})
			if err != nil {
				return err
			}
			fmt.Println(tok)
			out.Done(os.Stderr, "issued token %s; it is shown only this once", name)
			if url := strings.TrimRight(cfg.Service.PublicURL, "/"); url != "" {
				out.Hint(os.Stderr, "on the other machine, once ak is installed:")
				fmt.Fprintf(os.Stderr, "  ak remote add %s %s --token %s\n", name, url, tok)
			} else {
				out.Hint(os.Stderr, "next: ak remote add <name> <url> --token -; set service.public_url to print that line ready to paste")
			}
			return nil
		},
	}
	add.Flags().StringArrayVar(&routes, "route", nil, "limit the token to this route or account (repeatable)")
	_ = add.RegisterFlagCompletionFunc("route", completeRouteTargets)
	var asJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List the tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := cfg.Service.TokenNames()
			if asJSON {
				type tokenRow struct {
					Name    string    `json:"name"`
					Routes  []string  `json:"routes,omitempty"`
					Created time.Time `json:"created,omitzero"`
				}
				rows := make([]tokenRow, 0, len(names))
				for _, n := range names {
					t := cfg.Service.Tokens[n]
					rows = append(rows, tokenRow{Name: n, Routes: t.Routes, Created: t.Created})
				}
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			if len(names) == 0 {
				out.Empty(os.Stdout, "no tokens", "issue one with `ak serve token add <name>`")
				return nil
			}
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				t := cfg.Service.Tokens[n]
				rows = append(rows, []string{n, out.Or(strings.Join(t.Routes, ", ")), out.Or(created(t.Created))})
			}
			out.Table(os.Stdout, []string{"token", "routes", "created"}, rows)
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	revoke := &cobra.Command{
		Use:               "revoke <name>",
		ValidArgsFunction: completeTokens,
		Short:             "Revoke a token; a running service refuses it on its next request",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, ok := cfg.Service.Tokens[args[0]]; !ok {
				return fmt.Errorf("no token %q; `ak serve token list` lists them", args[0])
			}
			if err := core.ChangeService(cfg, func(s *config.Service) { delete(s.Tokens, args[0]) }); err != nil {
				return err
			}
			out.Done(os.Stdout, "revoked token %s", args[0])
			return nil
		},
	}
	cmd.AddCommand(add, list, revoke)
	return cmd
}

// created is when a token was issued, or "" when not recorded.
func created(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// newServeAfterCmd is `ak serve after`: what the service learned about its
// accounts, and the way to make it ask again.
func newServeAfterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "after",
		Short: "What each account refused, and how to forget it",
		Long: `A vendor that answers "this model is not on your plan" or "no such model"
says the same thing to every request after it. The service remembers that for a
day, so the next request is answered at once instead of asking again.

These are the refusals on record. Buying the model or changing the key makes
them wrong before they expire: ` + "`--forget`" + ` drops them all.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			forget, err := cmd.Flags().GetBool("forget")
			if err != nil {
				return err
			}
			dir, err := config.DataDir()
			if err != nil {
				return err
			}
			path := filepath.Join(dir, "capabilities.json")
			if forget {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				out.Done(os.Stdout, "forgot what the accounts refused")
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					out.Empty(os.Stdout, "nothing refused", "the service learns a refusal when a vendor answers one")
					return nil
				}
				return err
			}
			var rows []struct {
				Account  string `json:"account"`
				Model    string `json:"model"`
				Protocol string `json:"protocol"`
				Refusal  struct {
					Reason string    `json:"reason"`
					At     time.Time `json:"at"`
				} `json:"refusal"`
			}
			if err := json.Unmarshal(b, &rows); err != nil {
				return err
			}
			if len(rows) == 0 {
				out.Empty(os.Stdout, "nothing refused", "the service learns a refusal when a vendor answers one")
				return nil
			}
			table := make([][]string, 0, len(rows))
			for _, r := range rows {
				table = append(table, []string{r.Account, r.Model, r.Protocol, r.Refusal.Reason, r.Refusal.At.Local().Format("01-02 15:04")})
			}
			out.Table(os.Stdout, []string{"account", "model", "protocol", "reason", "seen"}, table)
			return nil
		},
	}
	cmd.Flags().Bool("forget", false, "drop every refusal, so the accounts are asked again")
	return cmd
}

func newServeRouteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "route",
		Aliases: []string{"routes"},
		Short:   "List, add and remove the service's routes",
	}

	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the service's routes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := cfg.Service.RouteNames()
			if asJSON {
				type routeRow struct {
					Name     string   `json:"name"`
					Kind     string   `json:"kind"`
					Model    string   `json:"model,omitempty"`
					Provider string   `json:"provider,omitempty"`
					Members  []string `json:"members,omitempty"`
					Strategy string   `json:"strategy,omitempty"`
				}
				rows := make([]routeRow, 0, len(names))
				for _, n := range names {
					r := cfg.Service.Routes[n]
					kind := "alias"
					strat := ""
					if r.IsPool() {
						kind = "pool"
						if r.Strategy != "" {
							strat = r.Strategy
						} else {
							strat = config.StrategyOrder
						}
					}
					rows = append(rows, routeRow{
						Name:     n,
						Kind:     kind,
						Model:    r.Model,
						Provider: r.Provider,
						Members:  r.Members,
						Strategy: strat,
					})
				}
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			if len(names) == 0 {
				out.Empty(os.Stdout, "no routes", "add one with `ak serve route add <name> --model <target>` or `--member <target>`")
				return nil
			}
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				r := cfg.Service.Routes[n]
				kind, strat, answers := "alias", "", r.Model
				if r.IsPool() {
					kind = "pool"
					strat = r.Strategy
					if strat == "" {
						strat = config.StrategyOrder
					}
					answers = strings.Join(r.Members, ", ")
					if r.Provider != "" {
						kind = "provider"
						if answers != "" {
							answers = r.Provider + " (" + answers + ")"
						} else {
							answers = r.Provider
						}
					}
				}
				rows = append(rows, []string{n, kind, out.Or(strat), out.Or(answers)})
			}
			out.Table(os.Stdout, []string{"route", "kind", "strategy", "answers with"}, rows)
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")

	var (
		model    string
		provider string
		strategy string
		members  []string
	)
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a service route (alias, pool, or provider pool)",
		Long: `Add or replace a named model route under [service.routes].

  ak serve route add fast --model command-code/deepseek-v4
  ak serve route add main --member command-code --member opendesign --strategy smart
  ak serve route add dpsk --provider dpsk`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			r := config.Route{
				Model:    strings.TrimSpace(model),
				Provider: strings.TrimSpace(provider),
				Members:  parseMemberList(members),
				Strategy: strings.TrimSpace(strategy),
			}
			if err := core.ChangeService(cfg, func(s *config.Service) {
				if s.Routes == nil {
					s.Routes = map[string]config.Route{}
				}
				s.Routes[name] = r
			}); err != nil {
				return err
			}
			out.Done(os.Stdout, "saved route %s", name)
			return nil
		},
	}
	add.Flags().StringVar(&model, "model", "", "target model name for an alias (<account>, <account>/<model>, or route)")
	add.Flags().StringVar(&provider, "provider", "", "existing provider pool whose members this route answers with")
	add.Flags().StringArrayVar(&members, "member", nil, "pool member target (repeatable)")
	add.Flags().StringVar(&strategy, "strategy", "", "pool strategy: order, rotate, least-used or smart")
	_ = add.RegisterFlagCompletionFunc("member", completeRouteTargets)
	_ = add.RegisterFlagCompletionFunc("provider", completePoolProviders)
	_ = add.RegisterFlagCompletionFunc("strategy", cobra.FixedCompletions([]string{"order", "rotate", "least-used", "smart"}, cobra.ShellCompDirectiveNoFileComp))

	rm := &cobra.Command{
		Use:               "rm <name>",
		Aliases:           []string{"remove"},
		ValidArgsFunction: completeRoutes,
		Short:             "Remove a service route",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Service.Routes[name]; !ok {
				return fmt.Errorf("no route %q; `ak serve route list` lists them", name)
			}
			if err := core.ChangeService(cfg, func(s *config.Service) { delete(s.Routes, name) }); err != nil {
				return err
			}
			out.Done(os.Stdout, "removed route %s", name)
			return nil
		},
	}

	cmd.AddCommand(list, add, rm)
	return cmd
}
