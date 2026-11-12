package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/vendor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// accountsPage lists the accounts: the vendor of each, who signed in, the
// providers on it and what is left of its plan or balance, read when the
// page is first shown and on b.
type accountsPage struct {
	cursor int
	quotas map[string]quota.Quota
	asked  bool
}

type accountQuotaMsg struct {
	account string
	q       quota.Quota
}

func newAccountsPage() *accountsPage { return &accountsPage{quotas: map[string]quota.Quota{}} }

func (p *accountsPage) title() string     { return "Accounts" }
func (p *accountsPage) capturing() bool   { return false }
func (p *accountsPage) consumesEsc() bool { return false }

func (p *accountsPage) help() []string {
	return []string{"a sign in", "enter sign in again", "n new provider", "d remove", "b balance"}
}

// shown reads the balances the first time the page is opened: a key away
// from the launcher, it is a request for them.
func (p *accountsPage) shown(a *app) tea.Cmd {
	if p.asked {
		return nil
	}
	return p.loadQuotas(a)
}

// loadQuotas asks every account's balance, each on its own.
func (p *accountsPage) loadQuotas(a *app) tea.Cmd {
	p.asked = true
	p.quotas = map[string]quota.Quota{}
	var cmds []tea.Cmd
	for _, name := range a.cfg.AccountNames() {
		pr, ok := a.cfg.AccountProvider(name)
		if !ok {
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			key, err := secrets.Default().Resolve(pr)
			if err != nil {
				return accountQuotaMsg{name, quota.Quota{Error: err.Error()}}
			}
			return accountQuotaMsg{name, quota.Query(context.Background(), name, pr, key)}
		})
	}
	return tea.Batch(cmds...)
}

func (p *accountsPage) selected(cfg *config.Config) (string, bool) {
	names := cfg.AccountNames()
	if len(names) == 0 {
		return "", false
	}
	p.cursor = clamp(p.cursor, 0, len(names)-1)
	return names[p.cursor], true
}

func (p *accountsPage) update(a *app, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case accountQuotaMsg:
		p.quotas[msg.account] = msg.q
		return nil
	case tea.KeyMsg:
		return p.keys(a, msg.String())
	}
	return nil
}

func (p *accountsPage) keys(a *app, k string) tea.Cmd {
	switch k {
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
		return nil
	case "down", "j":
		p.cursor++
		p.selected(a.cfg)
		return nil
	case "a":
		return a.open(newLoginOverlay(vendor.Vendor{}, "", nil))
	case "b":
		return p.loadQuotas(a)
	}
	name, ok := p.selected(a.cfg)
	if !ok {
		return nil
	}
	acc := a.cfg.Accounts[name]
	v, _ := vendor.Lookup(acc.Vendor)
	switch k {
	case "enter", "e":
		return a.open(newLoginOverlay(v, name, nil))
	case "n":
		d := NewDraft("")
		d.Kind, d.choice = v.Kinds()[0], choiceAccount+name
		return a.open(newFormOverlay(d))
	case "d":
		return a.open(newConfirmOverlay("Remove account "+name+" and its key?", func(a *app) tea.Cmd {
			if err := core.RemoveAccount(a.cfg, name); err != nil {
				return a.notify(err.Error(), true)
			}
			delete(p.quotas, name)
			return a.notify("removed account "+name, false)
		}))
	}
	return nil
}

func (p *accountsPage) view(a *app, width, height int) string {
	names := a.cfg.AccountNames()
	if len(names) == 0 {
		return dimStyle.Render("No accounts yet. Press a to sign in to a vendor; its providers then share one key and one balance.")
	}
	p.selected(a.cfg)
	cols := [][]string{{"ACCOUNT", "VENDOR", "USER", "PROVIDERS"}}
	for _, n := range names {
		acc := a.cfg.Accounts[n]
		vname := acc.Vendor
		if v, ok := vendor.Lookup(acc.Vendor); ok {
			vname = v.Name
		}
		users := a.cfg.UsersOf(n)
		cols = append(cols, []string{n, vname, dash(acc.User), dash(strings.Join(users, ", "))})
	}
	widths := make([]int, len(cols[0]))
	for _, c := range cols {
		for i, s := range c {
			widths[i] = max(widths[i], len(s))
		}
	}
	line := func(c []string) string {
		var parts []string
		for i, s := range c {
			parts = append(parts, fmt.Sprintf("%-*s", widths[i], s))
		}
		return strings.Join(parts, "  ")
	}

	var b strings.Builder
	b.WriteString(headStyle.Render("    "+line(cols[0])+"  PLAN") + "\n")
	for i, n := range names {
		r := row{picked: i == p.cursor}
		if r.picked {
			r.add(rowStyle, "▌ ")
		} else {
			r.add(rowStyle, "  ")
		}
		if a.cfg.Accounts[n].HasKey() {
			r.add(okStyle, "●")
		} else {
			r.add(errStyle, "●")
		}
		r.add(rowStyle, " "+line(cols[i+1])+"  ")
		st, text := p.plan(a, n)
		r.add(st, text)
		b.WriteString(r.render(width) + "\n")
	}
	return b.String()
}

// plan is what is left of an account, as far as its balance reading says.
func (p *accountsPage) plan(a *app, name string) (lipgloss.Style, string) {
	q, ok := p.quotas[name]
	switch {
	case !ok && p.asked:
		return dimStyle, a.spinner.View()
	case !ok:
		return dimStyle, "b to read"
	case q.NoSource:
		return dimStyle, "-"
	case q.Error != "":
		return errStyle, "✗ " + q.Error
	}
	return rowStyle, planText(q)
}

// planText is a balance and its plan windows on one line.
func planText(q quota.Quota) string {
	var parts []string
	if q.Balance != nil {
		s := strconv.FormatFloat(*q.Balance, 'f', 2, 64)
		if q.Currency != "" {
			s += " " + q.Currency
		}
		parts = append(parts, s)
	}
	for _, w := range q.Windows {
		s := fmt.Sprintf("%s %.0f%%", w.Name, w.Used)
		if w.Resets != nil {
			s += " ↻" + w.Resets.Local().Format("01-02 15:04")
		}
		parts = append(parts, s)
	}
	return dash(strings.Join(parts, " · "))
}
