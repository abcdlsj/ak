package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
)

func newServeTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Issue, list and revoke the service's tokens",
	}
	var routes []string
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Issue a token, printed once",
		Long: `Issue a token for one machine and print it once; only its hash is kept.
--route limits it to routes and accounts (repeatable); without it the
token may use every one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Service.Tokens[name]; ok {
				return fmt.Errorf("token %q exists; revoke it first to issue it again", name)
			}
			tok, hash, err := gateway.NewToken()
			if err != nil {
				return err
			}
			err = core.ChangeService(cfg, func(s *config.Service) {
				s.Tokens[name] = config.Token{Hash: hash, Routes: routes, Created: time.Now().UTC().Truncate(time.Second)}
			})
			if err != nil {
				return err
			}
			fmt.Println(tok)
			out.Done(os.Stderr, "issued token %s; it is shown only this once", name)
			if url := strings.TrimRight(cfg.Service.PublicURL, "/"); url != "" {
				out.Hint(os.Stderr, "on the other machine, once ak is installed:")
				fmt.Fprintf(os.Stderr, "  ak remote add %s %s --token %s\n", name, url, tok)
			} else {
				out.Hint(os.Stderr, "next: ak remote add <name> <url> --token -; set service.public_url to print that line ready to paste")
			}
			return nil
		},
	}
	add.Flags().StringArrayVar(&routes, "route", nil, "limit the token to this route or account (repeatable)")
	list := &cobra.Command{
		Use:   "list",
		Short: "List the tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := cfg.Service.TokenNames()
			if len(names) == 0 {
				out.Empty(os.Stdout, "no tokens", "issue one with `ak serve token add <name>`")
				return nil
			}
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				t := cfg.Service.Tokens[n]
				rows = append(rows, []string{n, out.Or(strings.Join(t.Routes, ", ")), out.Or(created(t.Created))})
			}
			out.Table(os.Stdout, []string{"token", "routes", "created"}, rows)
			return nil
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a token; a running service refuses it on its next request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, ok := cfg.Service.Tokens[args[0]]; !ok {
				return fmt.Errorf("no token %q; `ak serve token list` lists them", args[0])
			}
			if err := core.ChangeService(cfg, func(s *config.Service) { delete(s.Tokens, args[0]) }); err != nil {
				return err
			}
			out.Done(os.Stdout, "revoked token %s", args[0])
			return nil
		},
	}
	cmd.AddCommand(add, list, revoke)
	return cmd
}

// created is when a token was issued, or "" when not recorded.
func created(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// newServeAfterCmd is `ak serve after`: what the service learned about its
// accounts, and the way to make it ask again.
func newServeAfterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "after",
		Short: "What each account refused, and how to forget it",
		Long: `A vendor that answers "this model is not on your plan" or "no such model"
says the same thing to every request after it. The service remembers that for a
day, so the next request is answered at once instead of asking again.

These are the refusals on record. Buying the model or changing the key makes
them wrong before they expire: ` + "`--forget`" + ` drops them all.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			forget, err := cmd.Flags().GetBool("forget")
			if err != nil {
				return err
			}
			dir, err := config.DataDir()
			if err != nil {
				return err
			}
			path := filepath.Join(dir, "capabilities.json")
			if forget {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				out.Done(os.Stdout, "forgot what the accounts refused")
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					out.Empty(os.Stdout, "nothing refused", "the service learns a refusal when a vendor answers one")
					return nil
				}
				return err
			}
			var rows []struct {
				Account  string `json:"account"`
				Model    string `json:"model"`
				Protocol string `json:"protocol"`
				Refusal  struct {
					Reason string    `json:"reason"`
					At     time.Time `json:"at"`
				} `json:"refusal"`
			}
			if err := json.Unmarshal(b, &rows); err != nil {
				return err
			}
			if len(rows) == 0 {
				out.Empty(os.Stdout, "nothing refused", "the service learns a refusal when a vendor answers one")
				return nil
			}
			table := make([][]string, 0, len(rows))
			for _, r := range rows {
				table = append(table, []string{r.Account, r.Model, r.Protocol, r.Refusal.Reason, r.Refusal.At.Local().Format("01-02 15:04")})
			}
			out.Table(os.Stdout, []string{"account", "model", "protocol", "reason", "seen"}, table)
			return nil
		},
	}
	cmd.Flags().Bool("forget", false, "drop every refusal, so the accounts are asked again")
	return cmd
}

func newServeAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "The web UI's admin login",
	}
	set := &cobra.Command{
		Use:   "set-password",
		Short: "Set the web UI's password; only its bcrypt hash is kept",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pw, err := readPassword()
			if err != nil {
				return err
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
			if err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := core.ChangeService(cfg, func(s *config.Service) { s.AdminPassword = string(hash) }); err != nil {
				return err
			}
			out.Done(os.Stdout, "set the admin password")
			return nil
		},
	}
	cmd.AddCommand(set)
	return cmd
}

// minPassword is the shortest admin password taken.
const minPassword = 10

// readPassword reads a new password: twice without echo on a terminal, or
// the first line of a pipe.
func readPassword() (string, error) {
	var pw string
	if term.IsTerminal(os.Stdin.Fd()) {
		ask := func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(os.Stdin.Fd())
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		a, err := ask("New password: ")
		if err != nil {
			return "", err
		}
		b, err := ask("Again: ")
		if err != nil {
			return "", err
		}
		if a != b {
			return "", errors.New("the passwords differ")
		}
		pw = a
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len(pw) < minPassword {
		return "", fmt.Errorf("the password needs at least %d characters", minPassword)
	}
	return pw, nil
}
