package ui

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/usage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// usageRange is a selectable time window.
type usageRange struct {
	label string
	days  int // 0 means all history
}

var usageRanges = []usageRange{{"7 days", 7}, {"30 days", 30}, {"90 days", 90}, {"all", 0}}

// usagePage shows usage for a window, optionally narrowed to one provider.
type usagePage struct {
	rng      int
	cursor   int    // row in the provider table
	provider string // drill-down filter; empty means every provider
}

func newUsagePage() *usagePage { return &usagePage{rng: 1} }

func (p *usagePage) title() string { return "Usage" }

func (p *usagePage) capturing() bool { return false }

func (p *usagePage) consumesEsc() bool { return p.provider != "" }

func (p *usagePage) help() []string {
	h := []string{"[/] range", "r rescan"}
	if p.provider != "" {
		return append(h, "esc all providers")
	}
	return append(h, "↑/↓ provider", "enter focus")
}

func (p *usagePage) filter() usage.Filter {
	return usage.Filter{Since: usage.SinceDays(usageRanges[p.rng].days), Provider: p.provider}
}

func (p *usagePage) update(a *app, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "]", "right", "l":
		p.rng = (p.rng + 1) % len(usageRanges)
	case "[", "left", "h":
		p.rng = (p.rng + len(usageRanges) - 1) % len(usageRanges)
	case "r":
		return a.loadUsage()
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j":
		p.cursor++
	case "enter":
		if p.provider == "" {
			rows := usage.Summarize(a.usage.rows, a.cfg, p.filter()).ByProvider
			if len(rows) > 0 {
				p.provider = rows[clamp(p.cursor, 0, len(rows)-1)].Name
			}
		}
	case "esc", "backspace":
		p.provider = ""
	}
	return nil
}

func (p *usagePage) view(a *app, width, height int) string {
	switch {
	case a.usage.err != nil:
		return errStyle.Render(fmt.Sprintf("Failed to read usage: %v", a.usage.err))
	case !a.usage.loaded:
		return a.spinner.View() + dimStyle.Render(" Scanning session logs…  (the first scan reads the whole history)")
	}

	sum := usage.Summarize(a.usage.rows, a.cfg, p.filter())
	var b strings.Builder
	b.WriteString(p.rangeBar())
	b.WriteString("\n\n")
	if sum.TotalTokens == 0 {
		b.WriteString(dimStyle.Render("No usage recorded in this window."))
		return b.String()
	}

	b.WriteString(statTiles(sum))
	b.WriteString("\n\n")
	b.WriteString(section("Daily tokens", width))
	b.WriteString("\n")
	b.WriteString(renderHeatmap(buildHeatmap(sum.ByDate), width))
	b.WriteString("\n")

	used := lipgloss.Height(b.String())
	limit := clamp(height-used-3, 3, 12)

	// Side by side when there is room; stacked, each table gets the full width
	// and half the rows, so the models stay on a normal-height terminal.
	side := width >= 96
	tableW := width
	if side {
		tableW = width/2 - 2
	} else {
		limit = max(limit/2, 3)
	}
	right := modelTable(sum.ByModel, tableW, limit)
	if p.provider != "" {
		b.WriteString(modelTable(sum.ByModel, width, limit*2))
		return b.String()
	}
	p.cursor = clamp(p.cursor, 0, max(len(sum.ByProvider)-1, 0))
	left := providerTable(sum.ByProvider, tableW, limit, p.cursor)
	if side {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(width/2).Render(left), right))
	} else {
		b.WriteString(left + right)
	}
	return b.String()
}

// rangeBar shows the window choices and the drill-down, if any.
func (p *usagePage) rangeBar() string {
	parts := make([]string, len(usageRanges))
	for i, r := range usageRanges {
		if i == p.rng {
			parts[i] = chipActiveStyle.Render(r.label)
		} else {
			parts[i] = chipStyle.Render(r.label)
		}
	}
	bar := strings.Join(parts, " ")
	if p.provider != "" {
		bar += "   " + labelStyle.Render("provider ") + badgeStyle.Render(p.provider)
	}
	return bar
}

// statTiles is the headline row: the numbers someone opens this page for.
func statTiles(s usage.Summary) string {
	tiles := []string{tile("TOKENS", humanize(s.TotalTokens))}
	if s.TotalCost > 0 {
		tiles = append(tiles, tile("EST. COST", "$"+humanizeMoney(s.TotalCost)))
	}
	tiles = append(tiles,
		tile("REQUESTS", humanize(int64(s.Requests))),
		tile("ACTIVE DAYS", fmt.Sprintf("%d", len(s.ByDate))))

	// Join with a gap: flush borders read as one wide box instead of
	// separate numbers.
	spaced := make([]string, 0, len(tiles)*2-1)
	for i, t := range tiles {
		if i > 0 {
			spaced = append(spaced, "  ")
		}
		spaced = append(spaced, t)
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, spaced...)
	if s.UnpricedTokens > 0 {
		row += "\n" + dimStyle.Render(fmt.Sprintf("%s tokens have no pricing data and are left out of the cost.",
			humanize(s.UnpricedTokens)))
	}
	return row
}

func providerTable(rows []usage.ProviderRow, width, limit, cursor int) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(section("By provider", width))
	b.WriteString("\n")

	nameW := 14
	barW := max(width-nameW-24, 4)
	start := 0
	if cursor >= limit {
		start = cursor - limit + 1
	}
	for i := start; i < len(rows) && i < start+limit; i++ {
		r := rows[i]
		line := fmt.Sprintf("%-*s %8s %9s  %s", nameW, truncate(r.Name, nameW), humanize(r.Tokens), costCell(r.Cost),
			bar(r.Tokens, rows[0].Tokens, barW))
		if i == cursor {
			b.WriteString(pickStyle.Render("▌ " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}
	if more := len(rows) - start - limit; more > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", more)) + "\n")
	}
	return b.String()
}

func modelTable(rows []usage.ModelRow, width, limit int) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(section("By model", width))
	b.WriteString("\n")

	nameW := max(width-22, 12)
	for i, r := range rows {
		if i >= limit {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(rows)-limit)) + "\n")
			break
		}
		b.WriteString(fmt.Sprintf("  %-*s %8s %9s\n", nameW, truncate(r.Model, nameW), humanize(r.Tokens), costCell(r.Cost)))
	}
	return b.String()
}

func costCell(c float64) string {
	if c <= 0 {
		return dimStyle.Render(fmt.Sprintf("%9s", "-"))
	}
	return fmt.Sprintf("%9s", "$"+humanizeMoney(c))
}
