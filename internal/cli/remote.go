package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

// A remote is an ak service registered on a client as what ak already
// knows: a vendor in vendors.d whose engines are the service's endpoints,
// and an account on it holding the machine's token. Providers, bindings,
// the launcher and ak quota then work on it unchanged.

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Use an ak service on another machine as a vendor and account",
	}
	var token, model string
	var kinds []string
	add := &cobra.Command{
		Use:   "add <name> <url> --token -",
		Short: "Register an ak service, checking it takes the token",
		Long: `Register the ak service at url as the vendor and account <name>: claude
at the URL, codex and pi at <url>/v1, its quota read from the service.
The token, read from stdin with --token -, is the account's key; a remote
already registered takes no --token, its key is reused.

Given --model, it also puts a provider on the account for every engine, all
on that model name, so one command sets a machine up:

  ak remote add home https://ak.example.com --token - --model main
  ak home-claude          # or ak add --account home --kind claude --model main`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kinds != nil && model == "" {
				return errors.New("--kind picks the engines of a --model; pass --model too")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, base := args[0], strings.TrimRight(args[1], "/")
			if token == "-" {
				t, err := readKey()
				if err != nil {
					return err
				}
				token = t
			}
			if token == "" {
				token = keptToken(cfg, name, base)
			}
			if token == "" {
				return fmt.Errorf("no token; pass --token - and paste the one `ak serve token add` printed")
			}
			models, err := checkRemote(cmd.Context(), base, token)
			if err != nil {
				return err
			}
			if err := addRemote(cfg, name, base, token); err != nil {
				return err
			}
			out.Done(os.Stdout, "added remote %s at %s, %d model names", name, base, len(models))
			printRemoteModels(models)
			if model == "" {
				out.Hint(os.Stdout, "next: ak remote add %s %s --model <model>  puts claude, codex and pi on it at once", name, base)
				return nil
			}
			if !hasModel(models, model) {
				out.Hint(os.Stdout, "%q is not in the service's model list; it may still take it", model)
			}
			return addRemoteProviders(cfg, name, model, kinds)
		},
	}
	add.Flags().StringVar(&token, "token", "", "the service's token; - reads it from stdin")
	add.Flags().StringVar(&model, "model", "", "also add a provider per engine, all on this model name")
	add.Flags().StringArrayVar(&kinds, "kind", nil, "with --model, only these engines (claude, codex, pi; repeatable)")
	list := &cobra.Command{
		Use:   "list",
		Short: "List the registered services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			var rows [][]string
			for _, a := range cfg.AccountNames() {
				v, ok := vendor.Lookup(cfg.Accounts[a].Vendor)
				if !ok || v.Quota != quota.RemoteSource {
					continue
				}
				rows = append(rows, []string{a, v.WebsiteURL, out.Or(strings.Join(cfg.UsersOf(a), ", "))})
			}
			if len(rows) == 0 {
				out.Empty(os.Stdout, "no remotes", "add one with `ak remote add <name> <url> --token -`")
				return nil
			}
			out.Table(os.Stdout, []string{"remote", "url", "providers"}, rows)
			return nil
		},
	}
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a remote no provider runs on",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if v, ok := vendor.Lookup(cfg.Accounts[name].Vendor); !ok || v.Quota != quota.RemoteSource {
				return fmt.Errorf("no remote %q; `ak remote list` lists them", name)
			}
			if err := core.RemoveAccount(cfg, name); err != nil {
				return err
			}
			path, err := remotePath(name)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			out.Done(os.Stdout, "removed remote %s", name)
			return nil
		},
	}
	cmd.AddCommand(add, list, rm)
	return cmd
}

// remoteModel is one entry of the service's /v1/models: a model name the
// token may use, and how each protocol reaches it.
type remoteModel struct {
	ID string `json:"id"`
	AK struct {
		Protocols map[string]string `json:"protocols"`
	} `json:"ak"`
}

// checkRemote asks the service for the model names the token may use.
func checkRemote(ctx context.Context, base, token string) ([]remoteModel, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%q is no http(s) URL", base)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%s refused the token", base)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/v1/models answered %s; is it an ak service?", base, resp.Status)
	}
	var body struct {
		Data []remoteModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s/v1/models answered no model list: %w", base, err)
	}
	return body.Data, nil
}

// printRemoteModels lists the model names the token may use and, per engine's
// protocol, whether the service answers it natively or by translation.
func printRemoteModels(models []remoteModel) {
	if len(models) == 0 {
		out.Empty(os.Stdout, "The service listed no model names.", "")
		return
	}
	rows := make([][]string, 0, len(models))
	for _, m := range models {
		p := m.AK.Protocols
		rows = append(rows, []string{m.ID, out.Or(p["anthropic"]), out.Or(p["responses"]), out.Or(p["chat"])})
	}
	out.Table(os.Stdout, []string{"model", "claude", "codex", "pi"}, rows)
}

// hasModel reports whether the service listed name.
func hasModel(models []remoteModel, name string) bool {
	return slices.ContainsFunc(models, func(m remoteModel) bool { return m.ID == name })
}

// keptToken is the key already kept for remote name at base, so a remote
// registered once is set up further without the token again.
func keptToken(cfg *config.Config, name, base string) string {
	v, ok := vendor.Lookup(cfg.Accounts[name].Vendor)
	if !ok || v.Quota != quota.RemoteSource || strings.TrimRight(v.WebsiteURL, "/") != base {
		return ""
	}
	p, ok := cfg.AccountProvider(name)
	if !ok {
		return ""
	}
	key, err := secrets.Default().Resolve(p)
	if err != nil {
		return ""
	}
	return key
}

// addRemoteProviders puts a provider on the remote's account for each kind,
// all on model, and regenerates their commands. A provider already on it
// takes the model and keeps its other fields.
func addRemoteProviders(cfg *config.Config, remote, model string, kinds []string) error {
	if len(kinds) == 0 {
		kinds = []string{"claude", "codex", "pi"}
	}
	for _, kind := range kinds {
		if !slices.Contains([]string{"claude", "codex", "pi"}, kind) {
			return fmt.Errorf("%q is no engine; --kind takes claude, codex or pi", kind)
		}
	}
	changed := 0
	for _, kind := range kinds {
		n := remote + "-" + kind
		if old, exists := cfg.Providers[n]; exists {
			stored := cfg.Stored(old)
			if stored.Account != remote || stored.Kind != config.Kind(kind) {
				return fmt.Errorf("provider %q exists and is not %s on %s; remove it with `ak rm %s` first", n, kind, remote, n)
			}
			if stored.Model == model {
				continue
			}
			stored.Model = model
			if err := core.Update(cfg, n, stored); err != nil {
				return err
			}
			out.Done(os.Stdout, "edited %s (%s)", n, commandOf(cfg, n))
			changed++
			continue
		}
		if err := core.Add(cfg, n, config.Provider{Kind: config.Kind(kind), Account: remote, Model: model}); err != nil {
			return err
		}
		out.Done(os.Stdout, "added %s (%s)", n, commandOf(cfg, n))
		changed++
	}
	if changed == 0 {
		out.Hint(os.Stdout, "every provider was already on %s", model)
		return nil
	}
	if err := runSync(cfg, false); err != nil {
		return err
	}
	out.Hint(os.Stdout, "launch one with `%s`, or bind here with `ak bind`", commandOf(cfg, remote+"-"+kinds[0]))
	return nil
}

// remotePath is where a remote's vendor is kept.
func remotePath(name string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vendors.d", name+".toml"), nil
}

// addRemote writes the remote's vendor and signs its account in with the
// token. A vendor of that id that is not a remote is left alone.
func addRemote(cfg *config.Config, name, base, token string) error {
	if err := config.ValidateAccountName(name); err != nil {
		return err
	}
	if v, ok := vendor.Lookup(name); ok && v.Quota != quota.RemoteSource {
		return fmt.Errorf("vendor %q exists and is not a remote; pick another name", name)
	}
	v := vendor.Vendor{
		ID: name, Name: "ak at " + hostOnly(base), Category: "ak", WebsiteURL: base, Quota: quota.RemoteSource,
		// One endpoint per protocol, because that is what a service answers:
		// the same key and the same box speak all three.
		Endpoints: []vendor.Endpoint{
			{Protocol: vendor.ProtocolAnthropic, BaseURL: base},
			{Protocol: vendor.ProtocolResponses, BaseURL: base + "/v1"},
			{Protocol: vendor.ProtocolChat, BaseURL: base + "/v1"},
		},
	}
	data, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	path, err := remotePath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := config.AtomicWrite(path, data, 0o600); err != nil {
		return err
	}
	vendor.LoadDir(filepath.Dir(path))
	return core.SignIn(cfg, name, name, login.Credentials{Key: token})
}

func hostOnly(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}
