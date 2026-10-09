package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/abcdlsj/ak/internal/probe"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

func newModelsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "models <name>",
		Short: "List the models a provider serves",
		Long: `Ask the provider's upstream for its model list and print one id per line.

The endpoint is guessed from base_url: {base}/models when it already ends in a
version such as /v1, else {base}/v1/models and {base}/models; a base ending in
an Anthropic-compatible sub-path (/anthropic, /api/coding, ...) is also tried
on its root. The first that answers with a list wins.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			p, ok := cfg.Providers[name]
			if !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			key, err := secrets.Default().Resolve(p)
			if err != nil {
				return fmt.Errorf("%s: key: %w", name, err)
			}
			ids, err := probe.Models(cmd.Context(), p, key)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(ids)
			}
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}
