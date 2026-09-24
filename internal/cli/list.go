package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出所有供应商",
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

type listRow struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Kind    string `json:"kind"`
	Display string `json:"display,omitempty"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model,omitempty"`
	Default bool   `json:"default,omitempty"`
}

func listRows(cfg *config.Config) []listRow {
	rows := make([]listRow, 0, len(cfg.Providers))
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		rows = append(rows, listRow{
			Name:    name,
			Command: cfg.Settings.Prefix + name,
			Kind:    string(p.Kind),
			Display: p.Display,
			BaseURL: p.BaseURL,
			Model:   p.Model,
			Default: cfg.Settings.Default == name,
		})
	}
	return rows
}

func printList(cfg *config.Config) {
	rows := listRows(cfg)
	if len(rows) == 0 {
		fmt.Println("还没有配置供应商。用 `ak add` 添加,或 `ak import --from claude-settings` 从现有配置导入。")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "命令\t引擎\t模型\t端点\t说明")
	for _, r := range rows {
		mark := ""
		if r.Default {
			mark = " *"
		}
		fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\t%s\n",
			r.Command, mark, r.Kind, dash(r.Model), r.BaseURL, r.Display)
	}
	w.Flush()
	if cfg.Settings.Default != "" {
		fmt.Println("\n* 为默认供应商")
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
