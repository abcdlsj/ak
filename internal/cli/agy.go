package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/launch"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/spf13/cobra"
)

// newAgyCmd groups what is specific to Antigravity: agy signs in itself and
// keeps the account in the provider's own gemini_dir, so an "account" here is a
// directory and a credential file rather than a key ak hands out.
func newAgyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agy",
		Short: "Antigravity providers: create them, sign in, and read their account",
		Long: `Antigravity (agy) signs in through Google and keeps the credential inside the
provider's gemini_dir, so one provider is one account and two providers can run
at the same time.

  ak agy add work                        # a provider on ~/.local/share/ak/agy/work
  ak agy login work                      # sign in; the token lands in that directory
  ak agy import work --keychain          # or copy the account this Mac already has
  ak agy models work --set gemini-3.8-flash-high`,
	}
	cmd.AddCommand(newAgyAddCmd(), newAgyLoginCmd(), newAgyImportCmd(),
		newAgyTokenCmd(), newAgyModelsCmd(), newAgyPathCmd())
	return cmd
}

// defaultGeminiDir is where a provider's environment lives when the command
// line names no directory: one per provider, under ak's own data directory.
func defaultGeminiDir(name string) string {
	return config.DefaultHomeDir(config.KindAntigravity, name)
}

func newAgyAddCmd() *cobra.Command {
	var (
		dir, model, display string
		keychain, login     bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add an antigravity provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if dir == "" {
				dir = defaultGeminiDir(name)
			}
			p := config.Provider{Kind: config.KindAntigravity, GeminiDir: dir, Model: model, Display: display}
			if err := core.Add(cfg, name, p); err != nil {
				return err
			}
			// agy makes every other file itself; the directory has to exist
			// for the sign-in and for anything that lists it before then.
			if err := os.MkdirAll(config.ExpandHome(dir), 0o700); err != nil {
				return err
			}
			out.Done(os.Stdout, "added %s (%s)", name, commandOf(cfg, name))
			if keychain {
				if err := importAntigravityCredential(name, dir); err != nil {
					return err
				}
			}
			if err := runSync(cfg, false); err != nil {
				return err
			}
			if login {
				return execProviderToSignIn(cfg, name, "", nil)
			}
			if !keychain {
				out.Hint(os.Stdout, "Sign in with `ak agy login %s`, or copy this Mac's account in with `ak agy import %s --keychain`", name, name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "Directory holding this account (default ~/.local/share/ak/agy/<name>)")
	cmd.Flags().StringVar(&model, "model", "", "Model to launch with")
	cmd.Flags().StringVar(&display, "display", "", "Name shown in listings")
	cmd.Flags().BoolVar(&keychain, "keychain", false, "Copy the account this Mac is signed in to into the provider's directory")
	cmd.Flags().BoolVar(&login, "login", false, "Sign in right after adding")
	return cmd
}

// newAgyLoginCmd runs the provider's own launch: agy sees SSH_CONNECTION, keeps
// the credential in the provider's directory, and never touches the keyring the
// machine's own installation uses.
func newAgyLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "login <name>",
		Short:             "Sign in to an antigravity provider",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFirstTarget,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			p, ok := cfg.Providers[name]
			if !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			if p.Kind != config.KindAntigravity {
				return fmt.Errorf("provider %q is %s, not antigravity", name, p.Kind)
			}
			if err := os.MkdirAll(config.ExpandHome(p.GeminiDir), 0o700); err != nil {
				return err
			}
			out.Hint(os.Stdout, "agy opens a sign-in page and asks you to paste the code back; the account stays in %s", config.ExpandHome(p.GeminiDir))
			return execProviderToSignIn(cfg, name, "", nil)
		},
	}
}

func newAgyImportCmd() *cobra.Command {
	var (
		from     string
		keychain bool
	)
	cmd := &cobra.Command{
		Use:   "import <name>",
		Short: "Copy an Antigravity account into a provider's directory",
		Long: `Copy a credential agy already holds into a provider, so the account can run
beside the one the machine is signed in to.

  ak agy import work --keychain          # the account in this Mac's Keychain
  ak agy import work --from ~/.gemini    # a gemini_dir, its antigravity-cli/ taken as the source
  ak agy import work --from ./token.json # the credential document itself`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			p, ok := cfg.Providers[name]
			if !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			if p.Kind != config.KindAntigravity {
				return fmt.Errorf("provider %q is %s, not antigravity", name, p.Kind)
			}
			var raw []byte
			switch {
			case keychain && from != "":
				return fmt.Errorf("--keychain and --from are mutually exclusive")
			case keychain:
				raw, err = antigravityKeychainCredential()
			case from != "":
				raw, err = readAntigravityCredential(from)
			default:
				return fmt.Errorf("name a source: --keychain, or --from <file|gemini_dir>")
			}
			if err != nil {
				return err
			}
			dir := config.ExpandHome(p.GeminiDir)
			if err := provider.WriteAntigravityToken(dir, raw); err != nil {
				return err
			}
			return reportAntigravityToken(dir)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Credential document, or a gemini_dir to take antigravity-cli/ from")
	cmd.Flags().BoolVar(&keychain, "keychain", false, "Copy the account this Mac's Keychain holds")
	return cmd
}

// importAntigravityCredential is what --keychain does, for add.
func importAntigravityCredential(name, dir string) error {
	raw, err := antigravityKeychainCredential()
	if err != nil {
		return err
	}
	if err := provider.WriteAntigravityToken(config.ExpandHome(dir), raw); err != nil {
		return err
	}
	out.Done(os.Stdout, "%s now holds this Mac's Antigravity account", name)
	return reportAntigravityToken(config.ExpandHome(dir))
}

// antigravityKeychainCredential reads the credential agy stores in the OS
// keyring, in the form go-keyring wraps it in.
func antigravityKeychainCredential() ([]byte, error) {
	v, err := secrets.Default().Resolve(config.Provider{APIKeyRef: "keychain:gemini/antigravity"})
	if err != nil {
		return nil, fmt.Errorf("read this Mac's Antigravity account (agy stores it as keychain:gemini/antigravity): %w", err)
	}
	if strings.TrimSpace(v) == "" {
		return nil, fmt.Errorf("this Mac has no Antigravity account in its Keychain yet; sign in once with `agy`, or use `ak agy login`")
	}
	return []byte(v), nil
}

// readAntigravityCredential takes a credential document, or the
// antigravity-cli/antigravity-oauth-token of a gemini_dir.
func readAntigravityCredential(from string) ([]byte, error) {
	path := config.ExpandHome(from)
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = provider.AntigravityTokenPath(path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func newAgyTokenCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "token [name]",
		Short:             "Show an antigravity provider's account and credential state",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeFirstTarget,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				names = antigravityNames(cfg)
			}
			if len(names) == 0 {
				out.Empty(os.Stdout, "No antigravity providers.", "Add one with `ak agy add <name>`.")
				return nil
			}
			rows := make([]agyTokenRow, 0, len(names))
			for _, name := range names {
				row, err := antigravityTokenRow(cfg, name)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			printAgyTokens(rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

type agyTokenRow struct {
	Provider string `json:"provider"`
	Account  string `json:"account,omitempty"`
	Path     string `json:"path"`
	// State is ok, expiring, expired, or missing.
	State      string `json:"state"`
	Expiry     string `json:"expiry,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
}

// antigravityTokenRow summarizes one provider's credential, without ever
// printing the token itself.
func antigravityTokenRow(cfg *config.Config, name string) (agyTokenRow, error) {
	p, ok := cfg.Providers[name]
	if !ok {
		return agyTokenRow{}, fmt.Errorf("provider %q does not exist", name)
	}
	if p.Kind != config.KindAntigravity {
		return agyTokenRow{}, fmt.Errorf("provider %q is %s, not antigravity", name, p.Kind)
	}
	dir := config.ExpandHome(p.GeminiDir)
	row := agyTokenRow{Provider: name, Path: provider.AntigravityTokenPath(dir), State: "missing"}
	t, err := provider.ReadAntigravityToken(dir)
	if err != nil {
		return row, nil
	}
	row.Account, row.AuthMethod = t.Email, t.AuthMethod
	row.State = t.State(time.Now())
	if !t.Expiry.IsZero() {
		row.Expiry = t.Expiry.Local().Format(time.RFC3339)
	}
	return row, nil
}

func printAgyTokens(rows []agyTokenRow) {
	withAccount := false
	for _, r := range rows {
		withAccount = withAccount || r.Account != ""
	}
	head := []string{"provider", "state"}
	if withAccount {
		head = append(head, "account")
	}
	head = append(head, "expires", "path")
	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		row := []string{r.Provider, agyStateCell(r)}
		if withAccount {
			row = append(row, out.Or(r.Account))
		}
		row = append(row, out.Or(r.Expiry), r.Path)
		cells = append(cells, row)
	}
	out.Table(os.Stdout, head, cells)
	for _, r := range rows {
		if r.State == "missing" {
			out.Hint(os.Stdout, "%s has no credential; run `ak agy login %s`", r.Provider, r.Provider)
		}
	}
}

// agyStateCell marks a credential's state: fine, worth a look, or gone.
func agyStateCell(r agyTokenRow) string {
	switch r.State {
	case "ok":
		return out.OK("ok")
	case "expiring":
		return out.Warn("expiring")
	case "expired":
		// Not a problem by itself: agy refreshes with the refresh token.
		return out.Warn("expired")
	default:
		return out.Err("missing")
	}
}

// reportAntigravityToken says who the provider now signs in as.
func reportAntigravityToken(dir string) error {
	t, err := provider.ReadAntigravityToken(dir)
	if err != nil {
		return err
	}
	// agy records the address only when the sign-in left an ID token behind;
	// the credential without one is still a working account.
	who := t.Email
	if who == "" {
		who = "a credential agy can read (it carries no ID token, so ak cannot name the account)"
	}
	out.Done(os.Stdout, "%s holds %s", provider.AntigravityTokenPath(dir), who)
	return nil
}

func newAgyModelsCmd() *cobra.Command {
	var set string
	cmd := &cobra.Command{
		Use:   "models <name>",
		Short: "List the models an antigravity account serves",
		Long: `Ask agy which models this account serves; the list comes from the account's
plan, so two providers can differ. --set keeps one of them on the provider.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFirstTarget,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			p, ok := cfg.Providers[name]
			if !ok {
				return fmt.Errorf("provider %q does not exist", name)
			}
			if p.Kind != config.KindAntigravity {
				return fmt.Errorf("provider %q is %s, not antigravity", name, p.Kind)
			}
			models, err := antigravityModels(cfg, name)
			if err != nil {
				return err
			}
			if len(models) == 0 {
				out.Empty(os.Stdout, "agy named no models.", "")
				return nil
			}
			if set != "" {
				known := false
				for _, m := range models {
					known = known || m.ID == set
				}
				if !known {
					return fmt.Errorf("model %q is not one this account serves; see `ak agy models %s`", set, name)
				}
				p.Model = set
				if err := core.Update(cfg, name, p); err != nil {
					return err
				}
				out.Done(os.Stdout, "%s now launches with %s", name, set)
				return runSync(cfg, false)
			}
			rows := make([][]string, 0, len(models))
			for _, m := range models {
				rows = append(rows, []string{m.ID, m.Label})
			}
			out.Table(os.Stdout, []string{"model", "name"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "Keep this model on the provider")
	return cmd
}

type agyModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// antigravityModels runs agy's own model listing for a provider, with the same
// environment its launch gets, and parses its "id<TAB>label" lines.
func antigravityModels(cfg *config.Config, name string) ([]agyModel, error) {
	// The same plan a launch gets, with agy's own model listing as the
	// argument: the account is the directory, so the environment matters.
	plan, err := launch.Build(cfg, name, "", []string{"models"}, launch.Options{Environ: withoutOverride(os.Environ())})
	if err != nil {
		return nil, err
	}
	// Plan.Argv already starts with the binary.
	cmdExec := exec.Command(plan.Argv[0], plan.Argv[1:]...)
	cmdExec.Env = plan.Env
	cmdExec.Stderr = os.Stderr
	raw, err := cmdExec.Output()
	if err != nil {
		return nil, fmt.Errorf("agy models: %w", err)
	}
	var models []agyModel
	for _, line := range strings.Split(string(raw), "\n") {
		id, label, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || id == "" {
			continue
		}
		models = append(models, agyModel{ID: id, Label: label})
	}
	return models, nil
}

func newAgyPathCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "path <name>",
		Short: "Print where an antigravity provider keeps its files",
		Long: `Print the directories agy uses for a provider. Anything that has to install
into a provider's agy environment — a lifecycle hook, for one — needs the config
directory, which is not the machine's own ~/.gemini/config.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFirstTarget,
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
			if p.Kind != config.KindAntigravity {
				return fmt.Errorf("provider %q is %s, not antigravity", name, p.Kind)
			}
			dir := config.ExpandHome(p.GeminiDir)
			paths := map[string]string{
				"gemini_dir":   dir,
				"config_dir":   provider.AntigravityConfigPath(dir),
				"app_data_dir": provider.AntigravityAppDataPath(dir),
				"token":        provider.AntigravityTokenPath(dir),
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(paths)
			}
			for _, k := range []string{"gemini_dir", "config_dir", "app_data_dir", "token"} {
				fmt.Printf("%s\t%s\n", k, paths[k])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

// antigravityNames lists the antigravity providers, in config order.
func antigravityNames(cfg *config.Config) []string {
	var names []string
	for _, name := range cfg.Names() {
		if cfg.Providers[name].Kind == config.KindAntigravity {
			names = append(names, name)
		}
	}
	return names
}
