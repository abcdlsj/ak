package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// providersPage lists providers. As the home page it is only a launcher: the
// list, a number per row and enter. As the manage page it also shows the
// selected provider in detail and hosts the actions that change them.
type providersPage struct {
	manage    bool
	cursor    int
	filter    textinput.Model
	filtering bool
}

func newProvidersPage(manage bool) *providersPage {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter by name, model or endpoint"
	return &providersPage{manage: manage, filter: ti}
}

func (p *providersPage) title() string {
	if p.manage {
		return "Manage"
	}
	return "Launch"
}

func (p *providersPage) capturing() bool { return p.filtering }

func (p *providersPage) consumesEsc() bool { return p.filter.Value() != "" }

func (p *providersPage) help() []string {
	if p.filtering {
		return []string{"enter keep filter", "esc clear"}
	}
	if !p.manage {
		return []string{"enter/1-9 launch", "/ filter"}
	}
	return []string{"↑/↓ move", "enter edit", "/ filter", "a add", "d delete", "* default", "s sync"}
}

// focus puts the cursor on the named provider.
func (p *providersPage) focus(cfg *config.Config, name string) {
	for i, n := range p.visible(cfg) {
		if n == name {
			p.cursor = i
		}
	}
}

// visible returns the provider names matching the filter.
func (p *providersPage) visible(cfg *config.Config) []string {
	q := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	var out []string
	for _, n := range cfg.Names() {
		pr := cfg.Providers[n]
		hay := strings.ToLower(strings.Join([]string{n, pr.Display, pr.Model, pr.BaseURL, string(pr.Kind)}, " "))
		if q == "" || strings.Contains(hay, q) {
			out = append(out, n)
		}
	}
	return out
}

func (p *providersPage) selected(cfg *config.Config) (string, bool) {
	names := p.visible(cfg)
	if len(names) == 0 {
		return "", false
	}
	p.cursor = clamp(p.cursor, 0, len(names)-1)
	return names[p.cursor], true
}

func (p *providersPage) update(a *app, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if p.filtering {
		switch key.String() {
		case "esc":
			p.filter.SetValue("")
			fallthrough
		case "enter":
			p.filtering = false
			p.filter.Blur()
			return nil
		}
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(msg)
		p.cursor = 0
		return cmd
	}

	name, has := p.selected(a.cfg)
	switch key.String() {
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j":
		p.cursor++
		p.selected(a.cfg)
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = len(p.visible(a.cfg)) - 1
	case "/":
		p.filtering = true
		return p.filter.Focus()
	case "esc":
		p.filter.SetValue("")
	}
	if !p.manage {
		if key.String() == "a" && len(a.cfg.Providers) == 0 {
			return a.open(newFormOverlay(NewDraft(""), false))
		}
		return p.launcherKeys(a, key.String())
	}
	switch key.String() {
	case "a":
		return a.open(newFormOverlay(NewDraft(""), false))
	case "s":
		return a.sync()
	}
	if !has {
		return nil
	}

	switch key.String() {
	case "enter", "e":
		return a.open(newFormOverlay(EditDraft(name, a.cfg.Providers[name]), true))
	case "d":
		return a.open(newConfirmOverlay(
			fmt.Sprintf("Delete provider %s and its command %s%s?", name, a.cfg.Settings.Prefix, name),
			func(a *app) tea.Cmd {
				if err := core.Remove(a.cfg, name); err != nil {
					return a.notify(err.Error(), true)
				}
				return a.sync()
			}))
	case "*":
		target := name
		if a.cfg.Settings.Default == name {
			target = ""
		}
		if err := core.SetDefault(a.cfg, target); err != nil {
			return a.notify(err.Error(), true)
		}
		if target == "" {
			return a.notify("default cleared", false)
		}
		return a.notify("default provider: "+target, false)
	}
	return nil
}

// launcherKeys handles the home page: enter launches the selected provider, a
// digit launches the provider on that row, so a known provider is one key away.
func (p *providersPage) launcherKeys(a *app, key string) tea.Cmd {
	names := p.visible(a.cfg)
	switch {
	case key == "enter" && len(names) > 0:
		name, _ := p.selected(a.cfg)
		return launchProvider(a, name)
	case len(key) == 1 && key[0] >= '1' && key[0] <= '9':
		if i := int(key[0] - '1'); i < len(names) {
			p.cursor = i
			return launchProvider(a, names[i])
		}
	}
	return nil
}

// launchProvider launches name, asking for a variant first when it has any.
func launchProvider(a *app, name string) tea.Cmd {
	variants := variantNames(a.cfg, name)
	if len(variants) == 0 {
		return a.launch(Selection{Provider: name})
	}
	return a.open(newPickOverlay(name, variants))
}

// variantNames lists the variants the launcher offers for a provider.
func variantNames(cfg *config.Config, name string) []string {
	p := cfg.Providers[name]
	eng := provider.EngineFor(p.Kind)
	if eng == nil {
		return nil
	}
	l, err := eng.Launch(name, p, provider.Secret{}, provider.Context{})
	if err != nil {
		return nil
	}
	var out []string
	for _, v := range l.Variants {
		if !v.Quiet {
			out = append(out, v.Name)
		}
	}
	return out
}

func (p *providersPage) view(a *app, width, height int) string {
	if len(a.cfg.Providers) == 0 {
		if !p.manage {
			return dimStyle.Render("No providers yet. Press a to add one, or run `ak import --from cc-switch`.")
		}
		return dimStyle.Render("No providers yet. Press a to add one, or run `ak import --from cc-switch`.")
	}
	var top string
	if p.filtering || p.filter.Value() != "" {
		top = p.filter.View() + "\n\n"
	}
	names := p.visible(a.cfg)
	if len(names) == 0 {
		return top + dimStyle.Render("No provider matches the filter.")
	}
	name, _ := p.selected(a.cfg)
	if !p.manage {
		list := p.list(a, names, width, height-lipgloss.Height(top)-2)
		if pr := problemLine(a, name); pr != "" {
			list = strings.TrimRight(list, "\n") + "\n\n" + truncate(pr, width)
		}
		return top + list
	}

	// Side by side when there is room, detail below otherwise.
	if width >= 100 {
		listW := width * 11 / 20
		list := p.list(a, names, listW, height-lipgloss.Height(top))
		detail := detailPanel(a, name, width-listW-2)
		return top + lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(listW).Render(list), "  ", detail)
	}
	return top + p.list(a, names, width, height/2) + "\n" + detailPanel(a, name, width)
}

func (p *providersPage) list(a *app, names []string, width, height int) string {
	cfg := a.cfg
	nameW, kindW := len("PROVIDER"), len("KIND")
	for _, n := range names {
		nameW = max(nameW, lipgloss.Width(cfg.Settings.Prefix+n))
		kindW = max(kindW, len(cfg.Providers[n].Kind))
	}
	// Status mark, name, kind; the model takes what is left.
	modelW := max(width-nameW-kindW-10, 8)

	var b strings.Builder
	head := fmt.Sprintf("    %-*s  %-*s  %s", nameW, "PROVIDER", kindW, "KIND", "MODEL")
	if !p.manage {
		head = rowNumber(-1) + head
	}
	b.WriteString(headStyle.Render(head))
	b.WriteString("\n")

	// Keep the cursor in view when the list is taller than the page.
	rows := max(height-1, 3)
	start := 0
	if p.cursor >= rows {
		start = p.cursor - rows + 1
	}
	for i := start; i < len(names) && i < start+rows; i++ {
		n := names[i]
		pr := cfg.Providers[n]
		r := row{picked: i == p.cursor}
		// A caret and a fill: the fill alone is invisible on terminals that
		// ignore background colours.
		if r.picked {
			r.add(rowStyle, "▌ ")
		} else {
			r.add(rowStyle, "  ")
		}
		if !p.manage {
			r.add(badgeStyle, rowNumber(i))
		}
		st, mark := statusMark(a, n)
		r.add(st, mark)
		r.add(rowStyle, fmt.Sprintf(" %-*s  %-*s  %s", nameW, cfg.Settings.Prefix+n, kindW, pr.Kind, truncate(dash(pr.Model), modelW)))
		if cfg.Settings.Default == n {
			r.add(badgeStyle, " ★")
		}
		b.WriteString(r.render(width))
		b.WriteString("\n")
	}
	if len(names) > start+rows {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(names)-start-rows)))
	}
	return b.String()
}

// rowNumber is the launch digit for row i, blank past nine or for the header.
func rowNumber(i int) string {
	if i < 0 || i >= 9 {
		return "   "
	}
	return fmt.Sprintf("%d  ", i+1)
}

// statusMark is a one-cell health indicator; unknown until checks finish.
func statusMark(a *app, name string) (lipgloss.Style, string) {
	st, ok := a.statuses[name]
	switch {
	case !ok:
		return dimStyle, "·"
	case st.OK():
		return okStyle, "●"
	case st.NoEngine || st.NoKey:
		return errStyle, "●"
	default:
		return warnStyle, "●"
	}
}

// problemLine names what would make the provider's command fail, if anything.
func problemLine(a *app, name string) string {
	st, ok := a.statuses[name]
	if !ok || st.OK() {
		return ""
	}
	return errStyle.Render("✗ " + a.cfg.Settings.Prefix + name + ": " + strings.Join(st.Problems(), "; "))
}

// mappingSummary lists a pool's per-member model overrides, sorted by member.
func mappingSummary(p config.Provider) string {
	names := make([]string, 0, len(p.MemberModels))
	for m := range p.MemberModels {
		names = append(names, m)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, m := range names {
		parts = append(parts, m+"="+p.MemberModels[m])
	}
	return strings.Join(parts, ", ")
}

// detailPanel shows everything about one provider at a glance.
func detailPanel(a *app, name string, width int) string {
	p := a.cfg.Providers[name]
	inner := max(width-4, 10)

	var b strings.Builder
	title := name
	if p.Display != "" && p.Display != name {
		title += dimStyle.Render("  " + p.Display)
	}
	b.WriteString(titleStyle.Render(title) + "\n")
	b.WriteString(dimStyle.Render(truncate(core.CommandPath(a.cfg, name), inner)) + "\n\n")

	field := func(label, value string) {
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render(fmt.Sprintf("%-9s", label)), truncate(value, inner-10)))
	}
	field("engine", string(p.Kind))
	if p.IsPool() {
		field("members", strings.Join(p.Members, ", "))
		field("strategy", p.StrategyOrDefault())
		if len(p.MemberModels) > 0 {
			field("mapping", mappingSummary(p))
		}
	} else {
		field("endpoint", dash(p.BaseURL))
		field("key", keySummary(p))
	}
	field("model", dash(p.Model))
	if p.Quota != "" || p.QuotaCmd != "" {
		q := p.Quota
		if q == "" {
			q = "auto"
		}
		if p.QuotaCmd != "" {
			q = "script"
		}
		field("balance", q)
	}
	switch p.Kind {
	case config.KindClaude:
		if p.Opus != "" || p.Sonnet != "" || p.Haiku != "" {
			field("tiers", fmt.Sprintf("opus %s · sonnet %s · haiku %s", dash(p.Opus), dash(p.Sonnet), dash(p.Haiku)))
		}
	case config.KindCodex:
		field("wire api", dash(p.WireAPI))
		field("effort", dash(p.Reasoning))
	case config.KindPi:
		if p.PiProvider != "" {
			field("pi provider", p.PiProvider)
		} else {
			field("pi provider", provider.PiProviderID(name))
		}
		field("pi api", dash(p.PiAPI))
	}
	if vs := variantNames(a.cfg, name); len(vs) > 0 {
		field("variants", strings.Join(vs, " "))
	}

	b.WriteString("\n")
	b.WriteString(usageSummary(a, name))

	if p.IsPool() {
		if flow := poolFlow(a, name, inner); flow != "" {
			b.WriteString("\n" + flow + "\n")
		}
	}

	if st, ok := a.statuses[name]; ok && !st.OK() {
		b.WriteString("\n")
		for _, pr := range st.Problems() {
			b.WriteString(errStyle.Render("✗ "+pr) + "\n")
		}
	}
	return panelStyle.Width(width - 2).Render(strings.TrimRight(b.String(), "\n"))
}

func keySummary(p config.Provider) string {
	switch {
	case p.APIKeyRef != "":
		return "ref " + p.APIKeyRef
	case p.APIKey != "":
		return "set (" + maskKey(p.APIKey) + ")"
	default:
		return errStyle.Render("missing")
	}
}

func maskKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + "…" + k[len(k)-4:]
}

// usageSummary is the provider's last 30 days, once the logs are scanned.
func usageSummary(a *app, name string) string {
	switch {
	case a.usage.err != nil:
		return ""
	case !a.usage.loaded:
		return dimStyle.Render("usage: scanning…") + "\n"
	}
	s := usage.Summarize(a.usage.rows, a.cfg, usage.Filter{Since: usage.SinceDays(30), Provider: name})
	if s.TotalTokens == 0 {
		return dimStyle.Render("no usage in the last 30 days") + "\n"
	}
	line := fmt.Sprintf("%s tokens · %d requests", humanize(s.TotalTokens), s.Requests)
	if s.TotalCost > 0 {
		line += " · $" + humanizeMoney(s.TotalCost)
	}
	return labelStyle.Render("30 days  ") + line + "\n"
}

// open shows an overlay, running its initial command.
func (a *app) open(o overlay) tea.Cmd {
	a.overlay = o
	if i, ok := o.(interface{ init(*app) tea.Cmd }); ok {
		return i.init(a)
	}
	return nil
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
