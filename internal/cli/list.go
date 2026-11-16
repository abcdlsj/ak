package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(listRows(cfg))
			}
			printList(cfg)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
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
	Default      bool              `json:"default,omitempty"`
}

func listRows(cfg *config.Config) []listRow {
	rows := make([]listRow, 0, len(cfg.Providers))
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		rows = append(rows, listRow{
			Name:         name,
			Command:      cfg.Settings.Prefix + name,
			Kind:         string(p.Kind),
			Display:      p.Display,
			Account:      p.Account,
			BaseURL:      p.BaseURL,
			Model:        p.Model,
			PiProvider:   p.PiProvider,
			Members:      p.Members,
			Strategy:     p.Strategy,
			MemberModels: p.MemberModels,
			Default:      cfg.Settings.Default == name,
		})
	}
	return rows
}

func printList(cfg *config.Config) {
	rows := listRows(cfg)
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
		row := []string{cmd, r.Kind, out.Or(r.Model), out.Or(runsOn(r))}
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

// runsOn is what the command talks to: its account, the members of a pool,
// a provider pi knows, or the upstream URL.
func runsOn(r listRow) string {
	if r.Account != "" {
		return "account " + r.Account
	}
	return endpoint(r)
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
