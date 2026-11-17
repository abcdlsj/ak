package ui

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/vendor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// accountsPage lists what holds a balance, the accounts first, then the
// providers with an endpoint of their own that a balance source serves;
// the selected one's card is beside the list, as `ak quota board` draws it.
// The balances are read the first time the page is shown and again on r.
type accountsPage struct {
	cursor int
}

func newAccountsPage() *accountsPage { return &accountsPage{} }

func (p *accountsPage) title() string     { return "Accounts" }
func (p *accountsPage) capturing() bool   { return false }
func (p *accountsPage) consumesEsc() bool { return false }

func (p *accountsPage) help() []string {
	return []string{"a sign in", "enter sign in again", "m model", "n new provider", "d remove", "r refresh"}
}

// shown reads the balances the first time the page is opened: a key away
// from the launcher, it is a request for them.
func (p *accountsPage) shown(a *app) tea.Cmd {
	if a.quotas.started {
		a.quotas.reset(a.cfg)
		return nil
	}
	return a.quotas.read(a.cfg)
}

func (p *accountsPage) selected(a *app) (*card, bool) {
	cards := a.quotas.cards
	if len(cards) == 0 {
		return nil, false
	}
	p.cursor = clamp(p.cursor, 0, len(cards)-1)
	return cards[p.cursor], true
}

func (p *accountsPage) update(a *app, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
		return nil
	case "down", "j":
		p.cursor++
		p.selected(a)
		return nil
	case "home", "g":
		p.cursor = 0
		return nil
	case "end", "G":
		p.cursor = max(len(a.quotas.cards)-1, 0)
		return nil
	case "a":
		return a.open(newLoginOverlay(vendor.Vendor{}, "", nil))
	case "r":
		return a.quotas.read(a.cfg)
	}
	c, ok := p.selected(a)
	if !ok {
		return nil
	}
	name, isAccount := strings.CutPrefix(c.key, "account:")
	if !isAccount {
		// A provider of its own is edited on the home page.
		if k.String() == "enter" || k.String() == "e" {
			return a.open(newFormOverlay(EditDraft(a.cfg, name)))
		}
		return nil
	}
	v, _ := vendor.Lookup(a.cfg.Accounts[name].Vendor)
	switch k.String() {
	case "enter", "e":
		return a.open(newLoginOverlay(v, name, nil))
	case "m":
		return a.open(newAccountModelOverlay(name, a.cfg.Accounts[name].Model))
	case "n":
		kinds := v.Kinds()
		if len(kinds) == 0 {
			return a.notify(v.Name+" serves no engines", true)
		}
		d := NewDraft("")
		d.Kind = kinds[0]
		if len(kinds) == 1 {
			d.UseAccount(a.cfg, name)
			return a.open(&formOverlay{draft: d, picking: false})
		}
		d.choice = choiceAccount + name
		return a.open(newFormOverlay(d))
	case "d":
		return a.open(newConfirmOverlay("Remove account "+name+" and its key?", func(a *app) tea.Cmd {
			if err := core.RemoveAccount(a.cfg, name); err != nil {
				return a.notify(err.Error(), true)
			}
			a.quotas.reset(a.cfg)
			return a.notify("removed account "+name, false)
		}))
	}
	return nil
}

func (p *accountsPage) view(a *app, width, height int) string {
	cards := a.quotas.cards
	if len(cards) == 0 {
		return dimStyle.Render("No accounts yet. Press a to sign in to a vendor; its providers then share one key and one balance.")
	}
	sel, _ := p.selected(a)
	list := p.list(a, cards, max(width-cardWidth-2, 24), height)
	if width < cardWidth+28 {
		return list + "\n" + sel.view(a.spinner.View())
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(width-cardWidth-2).Render(list), "  ", sel.view(a.spinner.View()))
}

// list is one row per holder: its mark, name, what it is and who uses it,
// and the fullest window at a glance.
func (p *accountsPage) list(a *app, cards []*card, width, height int) string {
	nameW := len("ACCOUNT")
	for _, c := range cards {
		nameW = max(nameW, lipgloss.Width(c.title))
	}
	var b strings.Builder
	b.WriteString(headStyle.Render(fmt.Sprintf("    %-*s  %-6s  %s", nameW, "ACCOUNT", "USED", "FOR")) + "\n")
	rows := len(cards)
	if height > 3 {
		rows = max(height-2, 3)
	}
	start := 0
	if p.cursor >= rows {
		start = p.cursor - rows + 1
	}
	for i := start; i < len(cards) && i < start+rows; i++ {
		c := cards[i]
		r := row{picked: i == p.cursor}
		if r.picked {
			r.add(rowStyle, "▌ ")
		} else {
			r.add(rowStyle, "  ")
		}
		st, mark := holderMark(a.cfg, c)
		r.add(st, mark)
		r.add(rowStyle, " "+pad(c.title, nameW)+"  ")
		r.add(fullest(c, a.spinner.View()))
		r.add(dimStyle, "  "+truncate(usedBy(a.cfg, c), max(width-nameW-16, 8)))
		b.WriteString(r.render(width) + "\n")
	}
	if len(cards) > start+rows {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(cards)-start-rows)))
	}
	return b.String()
}

// holderMark is green when a reading came back, red when it failed or the
// account has no key, dim until then.
func holderMark(cfg *config.Config, c *card) (lipgloss.Style, string) {
	if name, ok := strings.CutPrefix(c.key, "account:"); ok && !cfg.Accounts[name].HasKey() {
		return errStyle, "●"
	}
	switch {
	case !c.read:
		return dimStyle, "·"
	case c.q.Error != "":
		return errStyle, "●"
	}
	return okStyle, "●"
}

// fullest is the share used of the holder's fullest window, coloured, six
// cells wide: the one number that says whether it can take more work.
func fullest(c *card, spin string) (lipgloss.Style, string) {
	switch {
	case c.reading && !c.read:
		return dimStyle, pad(spin, 6)
	case !c.read || c.q.NoSource:
		return dimStyle, pad("-", 6)
	case c.q.Error != "":
		return errStyle, pad("✗", 6)
	}
	most := -1.0
	for _, w := range c.q.Windows {
		most = max(most, w.Used)
	}
	if most < 0 {
		if c.q.Balance != nil {
			return rowStyle, pad(money(*c.q.Balance), 6)
		}
		return dimStyle, pad("-", 6)
	}
	return lipgloss.NewStyle().Foreground(usedColor(most, accentColor)), pad(fmt.Sprintf("%.0f%%", most), 6)
}

// usedBy is who uses the holder: an account's providers, or a provider's
// engine.
func usedBy(cfg *config.Config, c *card) string {
	name, ok := strings.CutPrefix(c.key, "account:")
	if !ok {
		return "own endpoint"
	}
	users := cfg.UsersOf(name)
	if len(users) == 0 {
		return "no providers yet"
	}
	return strings.Join(users, ", ")
}
