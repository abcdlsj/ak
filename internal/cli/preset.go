package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/preset"
	"github.com/spf13/cobra"
)

func newPresetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preset",
		Short: "Built-in vendor presets for `ak add --preset`",
	}
	cmd.AddCommand(newPresetListCmd())
	return cmd
}

func newPresetListCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the built-in presets",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ps := preset.All()
			if kind != "" {
				ps = preset.ForKind(config.Kind(kind))
				if len(ps) == 0 {
					return fmt.Errorf("no presets for kind %q; must be claude, codex or pi", kind)
				}
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "id\tkind\tname\tendpoint\tmodel\tkey url")
			for _, p := range ps {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					p.ID, p.Kind, p.Name, p.BaseURL, dash(p.Model), dash(p.APIKeyURL))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Only presets for this engine: claude, codex or pi")
	return cmd
}

// presetProvider starts a provider from --preset, then applies only the flags
// the user passed, so each one overrides the preset. The kind comes from
// --kind, or from the preset when it has a single engine.
func presetProvider(cmd *cobra.Command, id string) (config.Provider, error) {
	var kind config.Kind
	if cmd.Flags().Changed("kind") {
		v, _ := cmd.Flags().GetString("kind")
		kind = config.Kind(strings.TrimSpace(v))
	}
	ps, ok := preset.Lookup(id, kind)
	if !ok {
		kinds := preset.Kinds(id)
		switch {
		case len(kinds) == 0:
			return config.Provider{}, fmt.Errorf("unknown preset %q; see `ak preset list`", id)
		case kind == "":
			return config.Provider{}, fmt.Errorf("preset %q exists for %s; pass --kind", id, joinKinds(kinds))
		default:
			return config.Provider{}, fmt.Errorf("preset %q has no %s version; it exists for %s", id, kind, joinKinds(kinds))
		}
	}
	var p config.Provider
	ps.Apply(&p)
	applyProviderFlags(cmd, &p, true)
	return p, nil
}

func joinKinds(kinds []config.Kind) string {
	s := make([]string, len(kinds))
	for i, k := range kinds {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}
