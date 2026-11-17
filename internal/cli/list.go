package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var (
		asJSON  bool
		refresh bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all providers, and the models each really answers with",
		Long: `List every provider with what it runs on and the models it answers with.

A pool answers with its members' models, and a provider on a remote account
asks a name the service chose — possibly a route over several models. What
such a name answers with is what the service last said, kept between runs;
--refresh asks the services again.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			routes := core.RemoteRoutes(cfg)
			// What a remote said before is worth showing; a service that does
			// not answer leaves it in place rather than failing the list.
			if refresh || routes.Stale() {
				if fresh, err := core.FetchRemoteRoutes(cmd.Context(), cfg); err == nil {
					routes = fresh
				} else {
					warnf("ak: %v; remote models are as last seen", err)
				}
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(listRows(cfg, routes))
			}
			printList(cfg, routes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Ask remote services again what their names answer with")
	return cmd
}

type listRow struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Kind       string   `json:"kind"`
	Display    string   `json:"display,omitempty"`
	Account    string   `json:"account,omitempty"`
	BaseURL    string   `json:"base_url"`
	Model      string   `json:"model,omitempty"`
	PiProvider string   `json:"pi_provider,omitempty"`
	Members    []string `json:"members,omitempty"`
	Strategy   string   `json:"strategy,omitempty"`
	// MemberModels is a pool's per-member model, by member name.
	MemberModels map[string]string `json:"member_models,omitempty"`
	// Serves is what the provider really answers with: a pool's members'
	// models, and the models behind a name that only stands for them.
	Serves  []string `json:"serves,omitempty"`
	Default bool     `json:"default,omitempty"`
	// GeminiDir is an antigravity provider's own environment; AgyAccount is who
	// it is signed in as, both empty for the other engines.
	GeminiDir  string `json:"gemini_dir,omitempty"`
	AgyAccount string `json:"agy_account,omitempty"`
	// AgyState is ok, expiring, expired or missing, for the same providers.
	AgyState string `json:"agy_state,omitempty"`
	// HomeDir, NativeAccount and NativeState are set for directory-isolated
	// native-login providers of claude, codex and pi.
	HomeDir       string `json:"home_dir,omitempty"`
	NativeAccount string `json:"native_account,omitempty"`
	NativeState   string `json:"native_state,omitempty"`
}

func listRows(cfg *config.Config, routes core.RemoteModels) []listRow {
	rows := make([]listRow, 0, len(cfg.Providers))
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		account, state := agyCredential(p)
		var homeDir, nativeAccount, nativeState string
		if p.UsesEngineLogin() && p.Kind != config.KindAntigravity && p.HomeDir() != "" {
			homeDir = p.HomeDir()
			auth := provider.ReadNativeAuth(p)
			nativeAccount, nativeState = auth.Account, auth.State
		}
		rows = append(rows, listRow{
			Name:          name,
			Command:       cfg.Settings.Prefix + name,
			Kind:          string(p.Kind),
			Display:       p.Display,
			Account:       p.Account,
			BaseURL:       p.BaseURL,
			Model:         p.Model,
			PiProvider:    p.PiProvider,
			Members:       p.Members,
			Strategy:      p.Strategy,
			MemberModels:  p.MemberModels,
			Serves:        core.ModelNames(cfg, name, routes.Models),
			Default:       cfg.Settings.Default == name,
			GeminiDir:     p.GeminiDir,
			AgyAccount:    account,
			AgyState:      state,
			HomeDir:       homeDir,
			NativeAccount: nativeAccount,
			NativeState:   nativeState,
		})
	}
	return rows
}

func printList(cfg *config.Config, routes core.RemoteModels) {
	rows := listRows(cfg, routes)
	if len(rows) == 0 {
		out.Empty(os.Stdout, "No providers yet.", "Add one with `ak add`, sign in with `ak login <vendor>`, or `ak import --from cc-switch`.")
		return
	}
	// A display name is shown only where it says something the name does not,
	// and the column only when some provider has one.
	display := func(r listRow) string {
		if r.Display == r.Name {
			return ""
		}
		return r.Display
	}
	withDisplay := false
	for _, r := range rows {
		withDisplay = withDisplay || display(r) != ""
	}
	head := []string{"command", "engine", "model", "runs on"}
	if withDisplay {
		head = append(head, "display")
	}
	var cells [][]string
	for _, r := range rows {
		cmd := r.Command
		if r.Default {
			cmd += " " + out.Warn("★")
		}
		row := []string{cmd, r.Kind, modelCell(r), out.Or(runsOn(r))}
		if withDisplay {
			row = append(row, out.Or(display(r)))
		}
		cells = append(cells, row)
	}
	out.Table(os.Stdout, head, cells)
	if cfg.Settings.Default != "" {
		fmt.Println()
		out.Hint(os.Stdout, "★ the default provider, which the launcher opens on")
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// modelCell is what a provider answers with. A name that is not a model — a
// pool's, or a route a remote service chose — says the models behind it,
// because that is what the user is served by; the name itself stays when it
// is all ak can say.
func modelCell(r listRow) string {
	return out.Models(r.Model, r.Serves)
}

// runsOn is what the command talks to: its account, the members of a pool,
// a provider pi knows, or the upstream URL.
func runsOn(r listRow) string {
	if r.Account != "" {
		return "account " + r.Account
	}
	if r.GeminiDir != "" {
		return agyRunsOn(r)
	}
	if r.NativeState != "" {
		return nativeRunsOn(r)
	}
	return endpoint(r)
}

func nativeRunsOn(r listRow) string {
	switch {
	case r.NativeAccount != "":
		return r.Kind + " " + r.NativeAccount
	case r.NativeState == "missing":
		return r.Kind + " " + out.Warn("not signed in")
	case r.HomeDir != "":
		return r.Kind + " " + shortPath(config.ExpandHome(r.HomeDir))
	}
	return r.Kind
}

// agyRunsOn says which Google account a provider signs in as; without one the
// directory is the only thing there is to name.
func agyRunsOn(r listRow) string {
	switch {
	case r.AgyAccount != "":
		return "agy " + r.AgyAccount
	case r.AgyState == "missing" || r.AgyState == "":
		return "agy " + out.Warn("not signed in")
	}
	return "agy"
}

// membersOf lists a pool's members, each with the model the pool asks it for
// when that differs from the member's own.
func membersOf(r listRow) []string {
	out := make([]string, len(r.Members))
	for i, m := range r.Members {
		out[i] = m
		if model := r.MemberModels[m]; model != "" {
			out[i] = m + "=" + model
		}
	}
	return out
}

// endpoint is what the command talks to: the upstream URL, or the members of a
// pool.
func endpoint(r listRow) string {
	if len(r.Members) > 0 {
		strat := r.Strategy
		if strat == "" {
			strat = config.StrategyOrder
		}
		return "pool(" + strat + ": " + strings.Join(membersOf(r), ", ") + ")"
	}
	if r.BaseURL == "" && r.PiProvider != "" {
		return "pi:" + r.PiProvider
	}
	return r.BaseURL
}

// agyCredential is who an antigravity provider signs in as and what its
// credential's state is, from one read of its own environment. Both are empty
// for the other engines; a provider without a credential shows only that.
func agyCredential(p config.Provider) (account, state string) {
	if p.Kind != config.KindAntigravity || p.GeminiDir == "" {
		return "", ""
	}
	t, err := provider.ReadAntigravityToken(config.ExpandHome(p.GeminiDir))
	if err != nil {
		return "", "missing"
	}
	return t.Email, t.State(time.Now())
}

// agyAccount is who the provider signs in as, when the credential names it.
func agyAccount(p config.Provider) string {
	account, _ := agyCredential(p)
	return account
}

// agyState is what a listing shows about an antigravity credential without
// asking anyone: ok, expiring within a week, expired, or missing.
func agyState(p config.Provider) string {
	_, state := agyCredential(p)
	return state
}
