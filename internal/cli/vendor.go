package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/spf13/cobra"
)

func newVendorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "vendor",
		Aliases: []string{"preset"},
		Short:   "The vendors ak knows, for `ak login` and `ak add --vendor`",
	}
	cmd.AddCommand(newVendorListCmd())
	return cmd
}

func newVendorListCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the vendors and the engines each serves",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Loading the config reads the user's vendors in vendors.d.
			if _, err := loadConfig(); err != nil {
				return err
			}
			for _, err := range vendor.Errors() {
				warnf("ak: vendor skipped: %v", err)
			}
			if kind != "" {
				k, ok := parseKind(kind)
				if !ok {
					return fmt.Errorf("unknown engine %q; must be %s", kind, kindList())
				}
				printVendorKinds(vendor.All(), string(k))
				return nil
			}
			printVendors(vendor.All())
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Only vendors serving this engine: claude, codex or pi")
	return cmd
}

// printVendors lists every vendor, one row each, and the engines it serves.
func printVendors(vs []vendor.Vendor) {
	rows := make([][]string, len(vs))
	for i, v := range vs {
		rows[i] = []string{v.ID, v.Name, strings.Join(v.Kinds(), ", "), loginMethods(v)}
	}
	out.Table(os.Stdout, []string{"id", "name", "engines", "sign-in"}, rows)
}

// printVendorKinds lists the vendors serving kind, with what it runs on.
func printVendorKinds(vs []vendor.Vendor, kind string) {
	var rows [][]string
	for _, v := range vs {
		e, ok := v.Endpoint(kind)
		if !ok {
			continue
		}
		rows = append(rows, []string{v.ID, v.Name, out.Or(e.BaseURL), out.Or(e.Model), loginMethods(v)})
	}
	out.Table(os.Stdout, []string{"id", "name", "endpoint", "model", "sign-in"}, rows)
}

// loginMethods names a vendor's ways to sign in, pasting a key last.
func loginMethods(v vendor.Vendor) string { return methodNames(v.Logins()) }

func methodNames(ls []vendor.Login) string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Method
	}
	return strings.Join(out, ", ")
}

// lookupVendor finds a vendor or says which ids there are.
func lookupVendor(id string) (vendor.Vendor, error) {
	v, ok := vendor.Lookup(id)
	if !ok {
		return vendor.Vendor{}, fmt.Errorf("unknown vendor %q; see `ak vendor list`", id)
	}
	return v, nil
}

// vendorProvider starts a provider on an account of --vendor: --account, or
// the vendor's id, made with --key when it does not exist yet, its key
// replaced by --key when it does. The kind comes from --kind, or from the
// vendor when it serves one engine; the other flags the user passed
// override what the vendor gives.
func vendorProvider(cmd *cobra.Command, cfg *config.Config, id string) (config.Provider, error) {
	v, err := lookupVendor(id)
	if err != nil {
		return config.Provider{}, err
	}
	name, _ := cmd.Flags().GetString("account")
	if name = strings.TrimSpace(name); name == "" {
		name = v.ID
	}
	key, _ := cmd.Flags().GetString("key")
	a, exists := cfg.Accounts[name]
	switch {
	case exists && a.Vendor != v.ID:
		return config.Provider{}, fmt.Errorf("account %q is on %s, not %s; pass --account to name another", name, a.Vendor, v.ID)
	case !exists && key == "":
		return config.Provider{}, fmt.Errorf("no account %q yet: pass --key, or run `ak login %s` first", name, v.ID)
	}
	if key != "" {
		a.Vendor = v.ID
		a.SetKey(key)
		if err := core.SetAccount(cfg, name, a); err != nil {
			return config.Provider{}, err
		}
	}
	return accountProvider(cmd, cfg, name)
}

// accountProvider starts a provider on an existing account, of --kind or the
// one engine its vendor serves, with the flags the user passed applied over
// it. --key is the account's and is not applied.
func accountProvider(cmd *cobra.Command, cfg *config.Config, account string) (config.Provider, error) {
	a, ok := cfg.Accounts[account]
	if !ok {
		return config.Provider{}, fmt.Errorf("account %q does not exist; `ak accounts` lists them", account)
	}
	v, err := lookupVendor(a.Vendor)
	if err != nil {
		return config.Provider{}, err
	}
	kinds := v.Kinds()
	kind, _ := cmd.Flags().GetString("kind")
	switch {
	case cmd.Flags().Changed("kind"):
		if k, ok := parseKind(kind); ok {
			kind = string(k)
		}
		if !slices.Contains(kinds, kind) {
			return config.Provider{}, fmt.Errorf("%s serves %s, not %s", v.Name, strings.Join(kinds, ", "), kind)
		}
	case len(kinds) == 1:
		kind = kinds[0]
	default:
		return config.Provider{}, fmt.Errorf("%s serves %s; pass --kind", v.Name, strings.Join(kinds, ", "))
	}
	p := config.Provider{Kind: config.Kind(kind), Account: account}
	applyProviderFlags(cmd, &p, true, "kind", "key")
	return p, nil
}
