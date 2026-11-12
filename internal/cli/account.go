package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

func newLoginCmd() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:               "login <vendor> [account]",
		ValidArgsFunction: completeLogin,
		Short:             "Sign in to a vendor, making or renewing an account",
		Long: `Sign in to a vendor and keep the credentials as an account (named after the
vendor unless given). Every provider on the account runs with them, on
whichever engines the vendor serves.

  ak login command-code          # reads the Command Code CLI's sign-in
  ak login opendesign            # approve a code in the browser
  ak login kimi-coding --key -   # paste a key, out of shell history

Given no --method, ak offers the ways that work here: reading another tool's
sign-in when it has one, a device sign-in, or pasting a key.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := readKeyFlag(cmd); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			v, err := lookupVendor(args[0])
			if err != nil {
				return err
			}
			name := v.ID
			if len(args) == 2 {
				name = args[1]
			}
			key, _ := cmd.Flags().GetString("key")
			if err := signIn(cmd, cfg, v, name, method, key); err != nil {
				return err
			}
			if users := cfg.UsersOf(name); len(users) > 0 {
				fmt.Printf("providers on it: %s\n", strings.Join(users, ", "))
				return runSync(cfg, false)
			}
			fmt.Println("add a provider on it:")
			for _, k := range v.Kinds() {
				fmt.Printf("  ak add <name> --account %s --kind %s\n", name, k)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&method, "method", "", "How to sign in: file, device or paste")
	cmd.Flags().String("key", "", "The key to paste, a reference (env:NAME, cmd:..., keychain:...), or - to type it")
	_ = cmd.RegisterFlagCompletionFunc("method", cobra.FixedCompletions([]string{vendor.LoginFile, vendor.LoginDevice, vendor.LoginPaste}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func userSuffix(user string) string {
	if user == "" {
		return ""
	}
	return " (" + user + ")"
}

// signIn signs in to v as account name, by method or the one the user
// picks, with key when one was pasted already.
func signIn(cmd *cobra.Command, cfg *config.Config, v vendor.Vendor, name, method, key string) error {
	l, err := pickLogin(v, method, key != "")
	if err != nil {
		return err
	}
	c, err := runLogin(cmd.Context(), v, l, key)
	if err != nil {
		return err
	}
	if err := core.SignIn(cfg, name, v.ID, c); err != nil {
		return err
	}
	fmt.Printf("signed in to %s as account %s%s\n", v.Name, name, userSuffix(cfg.Accounts[name].User))
	return nil
}

// pickLogin chooses how to sign in: pasting when a key was given, the
// method named, the only one that works here, or the one the user picks.
func pickLogin(v vendor.Vendor, method string, haveKey bool) (vendor.Login, error) {
	ms := login.Methods(v)
	if haveKey {
		method = vendor.LoginPaste
	}
	if method != "" {
		i := slices.IndexFunc(ms, func(l vendor.Login) bool { return l.Method == method })
		if i < 0 {
			return vendor.Login{}, fmt.Errorf("%s cannot sign in by %s here; it can by %s", v.Name, method, methodNames(ms))
		}
		return ms[i], nil
	}
	if len(ms) == 1 || !term.IsTerminal(os.Stdin.Fd()) {
		return ms[0], nil
	}
	var i int
	opts := make([]huh.Option[int], len(ms))
	for j, l := range ms {
		opts[j] = huh.NewOption(login.Describe(l), j)
	}
	if err := runHuh(huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().Title("Sign in to " + v.Name).Options(opts...).Value(&i),
	))); err != nil {
		return vendor.Login{}, err
	}
	return ms[i], nil
}

// runLogin signs in by l; key is what was pasted, if anything.
func runLogin(ctx context.Context, v vendor.Vendor, l vendor.Login, key string) (login.Credentials, error) {
	switch l.Method {
	case vendor.LoginFile:
		return login.File(l)
	case vendor.LoginDevice:
		return login.Device(ctx, *l.Device, func(p login.Prompt) {
			fmt.Fprintf(os.Stderr, "Approve the sign-in at %s", p.URL)
			if p.Code != "" {
				fmt.Fprintf(os.Stderr, " (code %s)", p.Code)
			}
			fmt.Fprintln(os.Stderr, "; waiting…")
			login.OpenBrowser(p.URL)
		})
	}
	if key == "" {
		if v.APIKeyURL != "" {
			fmt.Fprintf(os.Stderr, "Get a key at %s\n", v.APIKeyURL)
		}
		var err error
		if key, err = readKey(); err != nil {
			return login.Credentials{}, err
		}
	}
	return login.Credentials{Key: key}, nil
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "logout <account>",
		ValidArgsFunction: completeAccounts,
		Short:             "Remove an account no provider runs on",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return core.RemoveAccount(cfg, args[0])
		},
	}
}

func newAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "List accounts: vendor, who signed in, key and the providers on each",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.Accounts) == 0 {
				fmt.Println("No accounts yet. Run `ak login <vendor>`; `ak vendor list` lists them.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "account\tvendor\tuser\tkey\tproviders")
			for _, n := range cfg.AccountNames() {
				a := cfg.Accounts[n]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n, a.Vendor, dash(a.User), accountKey(a), dash(strings.Join(cfg.UsersOf(n), ", ")))
			}
			return w.Flush()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:               "rename <account> <new-name>",
		ValidArgsFunction: completeAccounts,
		Short:             "Rename an account; its providers follow",
		Args:              cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return core.RenameAccount(cfg, args[0], args[1])
		},
	})
	return cmd
}

// accountKey says how an account authenticates, never the key itself.
func accountKey(a config.Account) string {
	switch {
	case a.APIKeyRef != "":
		return "ref " + a.APIKeyRef
	case a.APIKey != "":
		return "set"
	}
	return "missing"
}

// completeLogin completes a vendor, then an account of it.
func completeLogin(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeVendors(cmd, args, toComplete)
	case 1:
		return completeAccounts(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
