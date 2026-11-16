package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/out"
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
				out.Hint(os.Stdout, "providers on it: %s", strings.Join(users, ", "))
				return runSync(cfg, false)
			}
			fmt.Println()
			out.Hint(os.Stdout, "add a provider on it, one per engine:")
			for _, k := range v.Kinds() {
				out.Hint(os.Stdout, "  `ak add --account %s --kind %s`", name, k)
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
	out.Done(os.Stdout, "signed in to %s as %s%s", v.Name, name, userSuffix(cfg.Accounts[name].User))
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
		return login.Device(ctx, *l.Device, promptDevice)
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

// promptDevice asks the user to approve a device sign-in: the URL on its own
// line and the code bold, so both are easy to copy.
func promptDevice(p login.Prompt) {
	fmt.Fprintln(os.Stderr, "Approve the sign-in at")
	fmt.Fprintln(os.Stderr, p.URL)
	if p.Code != "" {
		fmt.Fprintln(os.Stderr, "code "+out.Bold(p.Code))
	}
	fmt.Fprintln(os.Stderr, out.Dim("waiting…"))
	login.OpenBrowser(p.URL)
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
				out.Empty(os.Stdout, "No accounts yet.", "Sign in with `ak login <vendor>`; `ak vendor list` lists the vendors.")
				return nil
			}
			rows := make([][]string, 0, len(cfg.Accounts))
			for _, n := range cfg.AccountNames() {
				a := cfg.Accounts[n]
				rows = append(rows, []string{n, a.Vendor, out.Or(a.User), accountKey(a),
					out.Or(a.Model), out.Or(strings.Join(cfg.UsersOf(n), ", "))})
			}
			out.Table(os.Stdout, []string{"account", "vendor", "user", "key", "model", "providers"}, rows)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "model <account> <model|->",
		Short: "Set the model an account's plan serves, or - to clear it",
		Long: `Say which model an account's plan serves, when the vendor's own default
is not one it has. Every provider on the account runs on it unless it names a
model of its own, and so does every endpoint, whatever protocol it speaks.

  ak accounts model command-code deepseek/deepseek-v4.1-flash
  ak accounts model opendesign -`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeAccounts,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			a, ok := cfg.Accounts[args[0]]
			if !ok {
				return fmt.Errorf("account %q does not exist", args[0])
			}
			model := strings.TrimSpace(args[1])
			if model == "-" {
				model = ""
			}
			if a.Model == model {
				out.Hint(os.Stdout, "%s already runs on %s", args[0], out.Or(model))
				return nil
			}
			a.Model = model
			if err := core.SetAccount(cfg, args[0], a); err != nil {
				return err
			}
			out.Done(os.Stdout, "%s runs on %s", args[0], out.Or(model))
			return nil
		},
	})
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
		return out.Mark(out.Fine) + " ref " + a.APIKeyRef
	case a.APIKey != "":
		return out.Mark(out.Fine) + " set"
	}
	return out.Mark(out.Broken) + " missing"
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
