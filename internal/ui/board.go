package ui

import (
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RunBoard shows every plan and balance as a card: each allowance window
// with what is used as a pixel number, a tick bar and when it resets. The
// cards are the accounts, then providers with an endpoint of their own
// that have a balance source.
func RunBoard(cfg *config.Config) error {
	_, err := tea.NewProgram(newBoard(cfg), tea.WithAltScreen()).Run()
	return err
}

// card is one thing with a balance: an account, or a provider on its own.
type card struct {
	key   string // the holder's, holderKey
	title string
	sub   string // the vendor, or the provider's engine
	p     config.Provider
	hue   lipgloss.AdaptiveColor

	q       quota.Quota
	read    bool
	readAt  time.Time
	reading bool
}

type board struct {
	cfg     *config.Config
	quotas  quotaStore
	spinner spinner.Model
	width   int
	height  int
	scroll  int
}

// boardTickMsg redraws the "read 2m ago" lines.
type boardTickMsg struct{}

func newBoard(cfg *config.Config) *board {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	b := &board{cfg: cfg, spinner: sp}
	b.quotas.reset(cfg)
	return b
}

// boardCards is a card per balance holder: each account, then each
// provider with an endpoint of its own that a balance source serves.
func boardCards(cfg *config.Config) []*card {
	holders, _ := core.Holders(cfg, nil)
	var out []*card
	for _, h := range holders {
		if !h.Account {
			if !quota.HasSource(h.Provider) {
				continue
			}
			out = append(out, &card{key: holderKey(false, h.Name), title: h.Name, sub: string(h.Provider.Kind) + " · " + hostOf(h.Provider.BaseURL), p: h.Provider, hue: hueOf(h.Name)})
			continue
		}
		acc := cfg.Accounts[h.Name]
		v, _ := vendor.Lookup(acc.Vendor)
		sub := v.Name
		if acc.User != "" {
			sub += " · " + acc.User
		}
		if acc.Model != "" {
			sub += " · " + acc.Model
		}
		if len(h.Users) > 0 {
			sub += " · " + strings.Join(h.Users, ", ")
		}
		out = append(out, &card{key: holderKey(true, h.Name), title: h.Name, sub: sub, p: h.Provider, hue: hueOf(v.ID)})
	}
	return out
}

func hostOf(base string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	host, _, _ := strings.Cut(s, "/")
	return host
}

// hues are the cards' colours; a card keeps its own across runs.
var hues = []lipgloss.AdaptiveColor{
	{Light: "#0969da", Dark: "#58a6ff"}, // blue
	{Light: "#8250df", Dark: "#a371f7"}, // violet
	{Light: "#1a7f37", Dark: "#3fb950"}, // green
	{Light: "#bf3989", Dark: "#db61a2"}, // pink
	{Light: "#0a7d8c", Dark: "#39c5cf"}, // teal
	{Light: "#953800", Dark: "#f0883e"}, // orange
}

func hueOf(key string) lipgloss.AdaptiveColor {
	h := fnv.New32a()
	h.Write([]byte(key))
	return hues[h.Sum32()%uint32(len(hues))]
}

func (b *board) Init() tea.Cmd {
	return tea.Batch(b.quotas.read(b.cfg), b.spinner.Tick, boardTick())
}

func boardTick() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return boardTickMsg{} })
}

func (b *board) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width, b.height = msg.Width, msg.Height
	case quotaReadMsg:
		b.quotas.got(msg)
	case boardTickMsg:
		return b, boardTick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		b.spinner, cmd = b.spinner.Update(msg)
		return b, cmd
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return b, tea.Quit
		case "r":
			return b, b.quotas.read(b.cfg)
		case "down", "j":
			b.scroll++
		case "up", "k":
			b.scroll = max(b.scroll-1, 0)
		case "pgdown", "ctrl+d":
			b.scroll += max(b.height-8, 4)
		case "pgup", "ctrl+u":
			b.scroll = max(b.scroll-max(b.height-8, 4), 0)
		case "home", "g":
			b.scroll = 0
		case "end", "G":
			b.scroll = math.MaxInt
		}
	}
	return b, nil
}

// cardWidth is a card's outer width; the board fits as many to a row as
// the terminal takes.
const cardWidth = 44

func (b *board) View() string {
	w := b.width
	if w <= 0 {
		w = 100
	}
	head := titleStyle.Render("ak quota") + dimStyle.Render("  ·  every plan at a glance")
	foot := dimStyle.Render("r refresh  ·  ↑/↓ scroll  ·  q quit")
	cards := b.quotas.shown()
	if len(cards) == 0 {
		return appStyle.Render(head + "\n\n" + dimStyle.Render("Nothing with a balance to show. Sign in with `ak login <vendor>`.") + "\n\n" + foot)
	}
	perRow := max(1, (w-appStyle.GetHorizontalPadding()+1)/(cardWidth+1))
	var rows []string
	for i := 0; i < len(cards); i += perRow {
		var line []string
		for _, c := range cards[i:min(i+perRow, len(cards))] {
			line = append(line, c.view(b.spinner.View()), " ")
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, line...))
	}
	body := strings.Join(rows, "\n")
	lines := strings.Split(body, "\n")
	if h := b.height - 6; h > 0 && len(lines) > h {
		b.scroll = min(b.scroll, len(lines)-h)
		foot = dimStyle.Render(fmt.Sprintf("r refresh  ·  ↑/↓ / pgup/pgdn scroll (%d-%d/%d)  ·  q quit", b.scroll+1, b.scroll+h, len(lines)))
		lines = lines[b.scroll : b.scroll+h]
	}
	return appStyle.Render(head + "\n\n" + strings.Join(lines, "\n") + "\n\n" + foot)
}

// inner is the text width inside a card.
const inner = cardWidth - 4

var cardStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lineColor).Padding(0, 1).Width(cardWidth - 2)

// view draws the card; spin is the spinner's frame for a reading under
// way.
func (c *card) view(spin string) string {
	hue := lipgloss.NewStyle().Foreground(c.hue)
	var s strings.Builder
	s.WriteString(spread(hue.Bold(true).Render("■ ")+lipgloss.NewStyle().Bold(true).Render(c.title), c.freshness(spin), inner) + "\n")
	s.WriteString(dimStyle.Render(truncate(c.sub, inner)) + "\n")

	switch q := c.q; {
	case !c.read:
		s.WriteString("\n" + spin + dimStyle.Render(" reading…") + "\n")
	case q.NoSource:
		s.WriteString("\n" + dimStyle.Render("no balance source for this vendor") + "\n")
	case q.Error != "":
		s.WriteString("\n" + errStyle.Render("✗ ") + wrap(out.Reason(q.Error), inner-2) + "\n")
	default:
		for _, w := range q.Windows {
			s.WriteString("\n" + window(w, c.hue))
		}
		if q.Balance != nil {
			s.WriteString("\n" + balance(q, c.hue))
		}
		if len(q.Accounts) > 0 {
			s.WriteString("\n" + dimStyle.Render(fmt.Sprintf("an ak service: %d accounts, a card each", len(q.Accounts))) + "\n")
		} else if len(q.Windows) == 0 && q.Balance == nil {
			s.WriteString("\n" + dimStyle.Render("the vendor reported nothing to show") + "\n")
		}
	}
	return cardStyle.Render(strings.TrimRight(s.String(), "\n"))
}

// freshness is when the card was read, or that it is being read.
func (c *card) freshness(spin string) string {
	switch {
	case c.reading:
		return spin
	case !c.read:
		return ""
	}
	d := time.Since(c.readAt)
	if d < time.Minute {
		return dimStyle.Render("↻ just now")
	}
	return dimStyle.Render("↻ " + short(d) + " ago")
}

// window is one allowance: its name and reset on the left, the share used
// as a pixel number on the right, a tick bar under them.
func window(w quota.Window, hue lipgloss.AdaptiveColor) string {
	on := lipgloss.NewStyle().Foreground(usedColor(w.Used, hue))
	num := pixels(strconv.Itoa(int(math.Round(w.Used))), on.Bold(true))
	left := [pixelRows]string{
		lipgloss.NewStyle().Bold(true).Render(w.Name),
		dimStyle.Render(resetText(w.Resets)),
		"",
	}
	var s strings.Builder
	for i := range num {
		s.WriteString(spread(left[i], num[i]+pctMark(i, on), inner) + "\n")
	}
	s.WriteString(ticks(w.Used/100, inner, on, lipgloss.NewStyle().Foreground(lineColor)) + "\n")
	return s.String()
}

// pctMark is the small % after a pixel number, on its last row.
func pctMark(row int, style lipgloss.Style) string {
	if row == pixelRows-1 {
		return style.Render(" %")
	}
	return "  "
}

// balance is money left, as a pixel number.
func balance(q quota.Quota, hue lipgloss.AdaptiveColor) string {
	style := lipgloss.NewStyle().Foreground(hue).Bold(true)
	num := pixels(strconv.FormatFloat(*q.Balance, 'f', 2, 64), style)
	left := [pixelRows]string{lipgloss.NewStyle().Bold(true).Render("Balance"), dimStyle.Render(q.Currency), ""}
	var s strings.Builder
	for i := range num {
		s.WriteString(spread(left[i], num[i], inner) + "\n")
	}
	if q.Used != nil && q.Limit != nil && *q.Limit > 0 {
		s.WriteString(ticks(*q.Used / *q.Limit, inner, style, lipgloss.NewStyle().Foreground(lineColor)) + "\n")
		s.WriteString(spread("", dimStyle.Render(fmt.Sprintf("%s of %s used", money(*q.Used), money(*q.Limit))), inner) + "\n")
	}
	return s.String()
}

func money(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// usedColor is the card's hue while a window has room, amber past 80% and
// red once it is spent.
func usedColor(used float64, hue lipgloss.AdaptiveColor) lipgloss.TerminalColor {
	switch {
	case used >= 100:
		return errColor
	case used >= 80:
		return warnColor
	}
	return hue
}

// resetText says when a window resets: in 2h 14m, or at 01-02 15:04 when
// that is days away.
func resetText(t *time.Time) string {
	if t == nil {
		return ""
	}
	d := time.Until(*t)
	switch {
	case d <= 0:
		return "↻ resetting"
	case d < 48*time.Hour:
		return "↻ in " + short(d)
	}
	return "↻ " + t.Local().Format("01-02 15:04")
}

// short is a duration to the minute: 2h 14m, 5m, 3d 4h.
func short(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", max(mins, 1))
}

// spread puts left and right at the two ends of a line width wide.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	return left + strings.Repeat(" ", max(gap, 1)) + right
}

// wrap breaks s into lines of at most width cells.
func wrap(s string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(s)
}
