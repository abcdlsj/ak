package ui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// quotaStore is what this run has read of every balance holder, shared by
// the pages: the Accounts page asks for it, the home page's detail shows it.
// Nothing is read until a page asks, so opening ak calls no balance API.
type quotaStore struct {
	cards   []*card
	byKey   map[string]*card
	started bool
}

type quotaReadMsg struct {
	key string
	q   quota.Quota
}

// holderKey keeps an account and a provider of the same name apart.
func holderKey(account bool, name string) string {
	if account {
		return "account:" + name
	}
	return "provider:" + name
}

// keyOf is the holder a provider's balance belongs to.
func keyOf(p config.Provider, name string) string {
	if p.Account != "" {
		return holderKey(true, p.Account)
	}
	return holderKey(false, name)
}

// reset lists the holders again, keeping what was read of those still there.
func (s *quotaStore) reset(cfg *config.Config) {
	old := s.byKey
	s.cards, s.byKey = boardCards(cfg), map[string]*card{}
	for _, c := range s.cards {
		if o, ok := old[c.key]; ok {
			c.q, c.read, c.readAt = o.q, o.read, o.readAt
		}
		s.byKey[c.key] = c
	}
}

// read asks every holder's balance, each on its own.
func (s *quotaStore) read(cfg *config.Config) tea.Cmd {
	s.reset(cfg)
	s.started = true
	var cmds []tea.Cmd
	for _, c := range s.cards {
		if c.reading {
			continue
		}
		c.reading = true
		cmds = append(cmds, readCard(c))
	}
	return tea.Batch(cmds...)
}

// readCard reads one card's balance.
func readCard(c *card) tea.Cmd {
	p, title, key := c.p, c.title, c.key
	return func() tea.Msg {
		k, err := secrets.Default().Resolve(p)
		if err != nil {
			return quotaReadMsg{key, quota.Quota{Error: err.Error()}}
		}
		return quotaReadMsg{key, quota.Query(context.Background(), title, p, k)}
	}
}

// shown is the cards the board draws: an ak service, once read, as a card
// for each of its accounts, in its place.
func (s *quotaStore) shown() []*card {
	var out []*card
	for _, c := range s.cards {
		if !c.read || len(c.q.Accounts) == 0 {
			out = append(out, c)
			continue
		}
		for _, q := range c.q.Accounts {
			out = append(out, &card{key: c.key + "/" + q.Provider, title: q.Provider, sub: c.title + " · " + dash(q.Source),
				p: c.p, hue: hueOf(q.Provider), q: q, read: true, readAt: c.readAt, reading: c.reading})
		}
	}
	return out
}

// got records a reading.
func (s *quotaStore) got(m quotaReadMsg) {
	if c, ok := s.byKey[m.key]; ok {
		c.q, c.read, c.reading, c.readAt = m.q, true, false, time.Now()
	}
}

// quotaOf is what was read of the holder of provider name, if anything.
func (a *app) quotaOf(name string) (quota.Quota, bool) {
	c, ok := a.quotas.byKey[keyOf(a.cfg.Providers[name], name)]
	if !ok || !c.read {
		return quota.Quota{}, false
	}
	return c.q, true
}

// shortReset is when a window resets, at most 7 cells: 2h 14m, or the date
// when that is days away.
func shortReset(t *time.Time) string {
	if t == nil {
		return ""
	}
	if d := time.Until(*t); d < 48*time.Hour {
		return short(max(d, 0))
	}
	return t.Local().Format("01-02")
}

// planBars is a reading as one line per window, a bar of ticks between the
// name and the share used, then a balance line: the detail panel's compact
// form of a card.
func planBars(q quota.Quota, width int) string {
	nameW := len("balance")
	for _, w := range q.Windows {
		nameW = max(nameW, lipgloss.Width(w.Name))
	}
	// name, bar, share and reset, each a space apart; a reset takes up to 9.
	barW := max(width-nameW-4-9-3, 6)
	var b strings.Builder
	for _, w := range q.Windows {
		on := lipgloss.NewStyle().Foreground(usedColor(w.Used, accentColor))
		pct := on.Render(fmt.Sprintf("%3d%%", int(math.Round(w.Used))))
		line := labelStyle.Render(pad(w.Name, nameW)) + " " + ticks(w.Used/100, barW, on, lipgloss.NewStyle().Foreground(lineColor)) + " " + pct
		if r := shortReset(w.Resets); r != "" {
			line += dimStyle.Render(" ↻ " + r)
		}
		b.WriteString(line + "\n")
	}
	if q.Balance != nil {
		b.WriteString(labelStyle.Render(pad("balance", nameW)) + " " + money(*q.Balance) + " " + dimStyle.Render(q.Currency) + "\n")
	}
	return b.String()
}
