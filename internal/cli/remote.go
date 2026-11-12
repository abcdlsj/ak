package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/quota"
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
	var token string
	add := &cobra.Command{
		Use:   "add <name> <url> --token -",
		Short: "Register an ak service, checking it takes the token",
		Long: `Register the ak service at url as the vendor and account <name>: claude
at the URL, codex and pi at <url>/v1, its quota read from the service.
The token, read from stdin with --token -, is the account's key. Then:

  ak add --account <name> --kind claude --model <route>`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "-" {
				t, err := readKey()
				if err != nil {
					return err
				}
				token = t
			}
			if token == "" {
				return errors.New("no token; pass --token - and paste the one `ak serve token add` printed")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, base := args[0], strings.TrimRight(args[1], "/")
			n, err := checkRemote(cmd.Context(), base, token)
			if err != nil {
				return err
			}
			if err := addRemote(cfg, name, base, token); err != nil {
				return err
			}
			out.Done(os.Stdout, "added remote %s at %s, %d model names", name, base, n)
			out.Hint(os.Stdout, "next: ak add --account %s --kind claude --model <route>", name)
			return nil
		},
	}
	add.Flags().StringVar(&token, "token", "", "the service's token; - reads it from stdin")
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

// checkRemote asks the service for the model names the token may use.
func checkRemote(ctx context.Context, base, token string) (int, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return 0, fmt.Errorf("%q is no http(s) URL", base)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return 0, fmt.Errorf("%s refused the token", base)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s/v1/models answered %s; is it an ak service?", base, resp.Status)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return strings.Count(string(b), `"id"`), nil
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
		Engines: map[string]vendor.Endpoint{
			"claude": {BaseURL: base},
			"codex":  {BaseURL: base + "/v1", WireAPI: "responses"},
			"pi":     {BaseURL: base, PiAPI: "anthropic-messages"},
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
