package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/config"
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
	Name     string   `json:"name"`
	Command  string   `json:"command"`
	Kind     string   `json:"kind"`
	Display  string   `json:"display,omitempty"`
	BaseURL  string   `json:"base_url"`
	Model    string   `json:"model,omitempty"`
	Members  []string `json:"members,omitempty"`
	Strategy string   `json:"strategy,omitempty"`
	Default  bool     `json:"default,omitempty"`
}

func listRows(cfg *config.Config) []listRow {
	rows := make([]listRow, 0, len(cfg.Providers))
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		rows = append(rows, listRow{
			Name:     name,
			Command:  cfg.Settings.Prefix + name,
			Kind:     string(p.Kind),
			Display:  p.Display,
			BaseURL:  p.BaseURL,
			Model:    p.Model,
			Members:  p.Members,
			Strategy: p.Strategy,
			Default:  cfg.Settings.Default == name,
		})
	}
	return rows
}

func printList(cfg *config.Config) {
	rows := listRows(cfg)
	if len(rows) == 0 {
		fmt.Println("No providers yet. Add one with `ak add`, or `ak import --from claude-settings`.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "command\tengine\tmodel\tendpoint\tdisplay")
	for _, r := range rows {
		mark := ""
		if r.Default {
			mark = " *"
		}
		fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\t%s\n",
			r.Command, mark, r.Kind, dash(r.Model), endpoint(r), r.Display)
	}
	w.Flush()
	if cfg.Settings.Default != "" {
		fmt.Println("\n* marks the default provider")
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// endpoint is what the command talks to: the upstream URL, or the members of a
// pool.
func endpoint(r listRow) string {
	if len(r.Members) > 0 {
		strat := r.Strategy
		if strat == "" {
			strat = config.StrategyOrder
		}
		return "pool(" + strat + ": " + strings.Join(r.Members, ", ") + ")"
	}
	return r.BaseURL
}
