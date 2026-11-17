package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/probe"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

func newModelsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "models <name>",
		ValidArgsFunction: completeFirstProvider,
		Short:             "List the models a provider serves",
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
			if p.Kind == config.KindAntigravity {
				models, err := antigravityModels(cfg, name)
				if err != nil {
					return err
				}
				ids := make([]string, len(models))
				for i, m := range models {
					ids[i] = m.ID
				}
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(ids)
				}
				printModels(ids)
				return nil
			}
			// A pool answers with its members' models, and a name a remote
			// service chose with the models it said it stands for; asking the
			// pool's own endpoint, which is ak's gateway, says nothing.
			if p.IsPool() || (p.Account != "" && core.RemoteRoutes(cfg).Models(p.Model) != nil) {
				routes := core.RemoteRoutes(cfg)
				if fresh, err := core.FetchRemoteRoutes(cmd.Context(), cfg); err == nil {
					routes = fresh
				}
				served := core.Serves(cfg, name, routes.Models)
				ids := make([]string, 0, len(served))
				for _, s := range served {
					ids = append(ids, s.Model)
				}
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(served)
				}
				printModels(ids)
				return nil
			}
			key, err := secrets.Default().Resolve(p)
			if err != nil {
				return fmt.Errorf("%s: key: %w", name, err)
			}
			ids, err := probe.Models(cmd.Context(), p, key)
			if err != nil {
				return fmt.Errorf("%s: %s", name, out.Reason(err.Error()))
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(ids)
			}
			printModels(ids)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

// printModels lists the ids the provider serves.
func printModels(ids []string) {
	if len(ids) == 0 {
		out.Empty(os.Stdout, "The provider listed no models.", "")
		return
	}
	rows := make([][]string, len(ids))
	for i, id := range ids {
		rows[i] = []string{id}
	}
	out.Table(os.Stdout, []string{"model"}, rows)
}
