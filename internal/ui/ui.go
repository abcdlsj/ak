// Package ui is ak's TUI, with two pages: Providers and Usage.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Run starts the TUI. Returns the provider name the user chose to launch;
// empty means they only quit.
func Run(cfg *config.Config) (string, error) {
	m := newModel(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	if fm, ok := final.(Model); ok {
		return fm.Selected(), nil
	}
	return "", nil
}

// RunUI is the entry point called from the cli: enter the TUI, then exec the
// provider the user picked.
func RunUI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		fmt.Println("No providers configured yet. Run `ak add`, or `ak import --from claude-settings`.")
		return nil
	}
	selected, err := Run(cfg)
	if err != nil {
		return err
	}
	if selected == "" {
		return nil
	}
	// Launch the chosen provider. syscall.Exec lets the engine take over the
	// current terminal and process, leaving no extra intermediate process behind.
	bin := filepath.Join(config.ExpandHome(cfg.Settings.BinDir),
		cfg.Settings.Prefix+selected)
	argv := append([]string{bin}, os.Args[1:]...)
	return syscall.Exec(bin, argv, os.Environ())
}

// page is one of the TUI's two tabs.
type page int

const (
	pageProviders page = iota
	pageUsage
)

// Model is the TUI state.
type Model struct {
	cfg       *config.Config
	page      page
	width     int
	height    int
	cursor    int
	usage     usage.Summary
	usageErr  error
	loading   bool
	usageDone bool
	quitting  bool
	// A non-empty selected means the user picked a provider to launch.
	selected string
}

// Selected returns the provider the user chose to launch; empty means quit only.
func (m Model) Selected() string { return m.selected }

type usageLoadedMsg struct {
	sum usage.Summary
	err error
}

func newModel(cfg *config.Config) Model {
	return Model{cfg: cfg, page: pageProviders}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit

		case "tab", "right", "l":
			if m.page == pageProviders {
				return m.showUsage()
			}
			m.page = pageProviders
			return m, nil

		case "shift+tab", "left", "h":
			m.page = pageProviders
			return m, nil

		case "1":
			m.page = pageProviders
			return m, nil

		case "2":
			return m.showUsage()

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "down", "j":
			if m.cursor < len(m.cfg.Providers)-1 {
				m.cursor++
			}
			return m, nil

		case "enter":
			if m.page == pageProviders {
				m.selected = m.cfg.Names()[m.cursor]
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}

	case usageLoadedMsg:
		m.usage, m.usageErr = msg.sum, msg.err
		m.loading, m.usageDone = false, true
		return m, nil
	}
	return m, nil
}

// showUsage switches to the usage page, scanning the logs the first time only:
// a full history scan takes seconds, and a re-scan on every tab press would
// make the page feel stuck.
func (m Model) showUsage() (tea.Model, tea.Cmd) {
	m.page = pageUsage
	if m.usageDone || m.loading {
		return m, nil
	}
	m.loading = true
	return m, m.loadUsage()
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	w := m.contentWidth()

	var body string
	switch m.page {
	case pageProviders:
		body = m.providersView(w)
	case pageUsage:
		body = m.usageView(w)
	}

	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		m.tabBar(w),
		"",
		body,
		"",
		m.footer(w),
	))
}

// tabBar shows the product name and the two pages, with a rule under them so
// the header reads as one band rather than a loose line of text.
func (m Model) tabBar(width int) string {
	tab := func(label string, p page) string {
		if m.page == p {
			return tabActiveStyle.Render(label)
		}
		return tabInactiveStyle.Render(label)
	}
	row := lipgloss.JoinHorizontal(lipgloss.Bottom,
		titleStyle.Render("ak"),
		"  ",
		tab("Providers", pageProviders),
		tab("Usage", pageUsage),
	)
	rule := width
	if rule < 1 {
		rule = 1
	}
	return row + "\n" + ruleStyle.Render(strings.Repeat("─", rule))
}

// footer is the key hint line, kept to the keys that do something on the
// current page.
func (m Model) footer(width int) string {
	keys := []string{"tab switch", "q quit"}
	if m.page == pageProviders {
		keys = []string{"↑/↓ move", "enter launch", "tab usage", "q quit"}
	}
	return dimStyle.Render(strings.Join(keys, "   ·   "))
}

func (m Model) providersView(width int) string {
	names := m.cfg.Names()
	if len(names) == 0 {
		return dimStyle.Render("No providers configured. Run `ak add`.")
	}

	// Size the name column to the data: a fixed width either clips long names
	// or leaves a gap that detaches the columns from each other.
	nameW, kindW, modelW := len("PROVIDER"), len("KIND"), len("MODEL")
	for _, n := range names {
		if l := len(m.cfg.Settings.Prefix + n); l > nameW {
			nameW = l
		}
		if l := len(m.cfg.Providers[n].Kind); l > kindW {
			kindW = l
		}
		if l := len(dash(m.cfg.Providers[n].Model)); l > modelW {
			modelW = l
		}
	}

	var b strings.Builder
	b.WriteString(headStyle.Render(fmt.Sprintf("  %-*s  %-*s  %s",
		nameW, "PROVIDER", kindW, "KIND", "MODEL")))
	b.WriteString("\n")

	for i, n := range names {
		p := m.cfg.Providers[n]
		def := ""
		if m.cfg.Settings.Default == n {
			def = "  default"
		}
		// The badge belongs inside the row text: appended after a full-width
		// highlight it would land past the fill and wrap onto the next line.
		line := fmt.Sprintf("  %-*s  %-*s  %-*s%s",
			nameW, m.cfg.Settings.Prefix+n, kindW, p.Kind, modelW, dash(p.Model), def)

		style := rowStyle
		if i == m.cursor {
			// Mark the row with both a caret and a fill: the fill alone is
			// invisible on terminals that ignore background colours.
			line = "▌" + strings.TrimPrefix(line, " ")
			style = pickStyle.MaxWidth(width).Width(width)
		}
		b.WriteString(style.Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) usageView(width int) string {
	switch {
	case m.usageErr != nil:
		return fmt.Sprintf("Failed to read usage: %v", m.usageErr)
	case m.loading:
		return dimStyle.Render("Scanning session logs…  (the first scan reads the whole history)")
	case m.usage.TotalTokens == 0:
		return dimStyle.Render("No usage recorded on this machine yet.")
	}

	var b strings.Builder
	b.WriteString(m.statTiles())
	b.WriteString("\n\n")

	b.WriteString(section("Daily tokens", width))
	b.WriteString("\n")
	b.WriteString(renderHeatmap(buildHeatmap(m.usage.ByDate), width))
	b.WriteString("\n")

	// Side by side when there is room; the two tables are short and stacking
	// them pushes the models off a normal-height terminal.
	limit := m.rowBudget()
	left := m.providerTable(width/2-2, limit)
	right := m.modelTable(width/2-2, limit)
	if width >= 96 && left != "" && right != "" {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(width/2).Render(left), right))
		return b.String()
	}
	b.WriteString(left)
	if left != "" && right != "" {
		b.WriteString("\n")
	}
	b.WriteString(right)
	return b.String()
}

// statTiles is the headline row: the numbers someone opens this page for.
func (m Model) statTiles() string {
	tiles := []string{tile("TOKENS", humanize(m.usage.TotalTokens))}
	if m.usage.TotalCost > 0 {
		tiles = append(tiles, tile("EST. COST", fmt.Sprintf("$%s", humanizeMoney(m.usage.TotalCost))))
	}
	tiles = append(tiles, tile("ACTIVE DAYS", fmt.Sprintf("%d", len(m.usage.ByDate))))

	// Join with a gap: flush borders read as one wide box instead of three
	// separate numbers.
	spaced := make([]string, 0, len(tiles)*2-1)
	for i, t := range tiles {
		if i > 0 {
			spaced = append(spaced, "  ")
		}
		spaced = append(spaced, t)
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, spaced...)
	if m.usage.UnpricedTokens > 0 {
		row += "\n" + dimStyle.Render(fmt.Sprintf("%s tokens have no pricing data and are left out of the cost.",
			humanize(m.usage.UnpricedTokens)))
	}
	return row
}

func (m Model) providerTable(width, limit int) string {
	rows := m.usage.ByProvider
	if len(rows) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(section("By provider", width))
	b.WriteString("\n")

	nameW := 14
	barW := width - nameW - 24
	if barW < 4 {
		barW = 4
	}
	for i, r := range rows {
		if i >= limit {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(rows)-limit)))
			b.WriteString("\n")
			break
		}
		b.WriteString(fmt.Sprintf("  %-*s %8s %9s  %s\n",
			nameW, truncate(r.Name, nameW), humanize(r.Tokens), costCell(r.Cost),
			bar(r.Tokens, rows[0].Tokens, barW)))
	}
	return b.String()
}

func (m Model) modelTable(width, limit int) string {
	rows := m.usage.ByModel
	if len(rows) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(section("By model", width))
	b.WriteString("\n")

	nameW := width - 20
	if nameW < 12 {
		nameW = 12
	}
	for i, r := range rows {
		if i >= limit {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(rows)-limit)))
			b.WriteString("\n")
			break
		}
		b.WriteString(fmt.Sprintf("  %-*s %8s %9s\n",
			nameW, truncate(r.Model, nameW), humanize(r.Tokens), costCell(r.Cost)))
	}
	return b.String()
}

// rowBudget is how many table rows fit under the heatmap.
//
// The tiles and the grid have a fixed height, so on a short terminal it is the
// tables that have to give; without a budget they push the page past the bottom
// and the last rows are simply lost.
func (m Model) rowBudget() int {
	const (
		fixed = 28 // padding, tabs, tiles, section heads, grid, legend, footer
		most  = 8
		least = 3
	)
	if m.height <= 0 {
		return most
	}
	n := m.height - fixed
	if n > most {
		return most
	}
	if n < least {
		return least
	}
	return n
}

// contentWidth is the space inside the app padding.
//
// WindowSizeMsg has not arrived on the very first frame; fall back to a
// conservative width rather than assuming an arbitrarily wide terminal.
func (m Model) contentWidth() int {
	const fallback = 96
	w := m.width
	if w <= 0 {
		w = fallback
	}
	h := appStyle.GetHorizontalPadding()
	if w-h < 20 {
		return 20
	}
	return w - h
}

func costCell(c float64) string {
	if c <= 0 {
		return dimStyle.Render("       -")
	}
	return fmt.Sprintf("$%s", humanizeMoney(c))
}

func truncate(s string, n int) string {
	if n <= 1 || len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (m Model) loadUsage() tea.Cmd {
	return func() tea.Msg {
		sum, err := usage.Aggregate(m.cfg)
		return usageLoadedMsg{sum: sum, err: err}
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanize(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// humanizeMoney keeps big totals short; cents only matter on small ones.
func humanizeMoney(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", v/1e3)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}
