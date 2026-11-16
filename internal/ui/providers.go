package ui

import (
	"cmp"
	"fmt"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// providersPage is the home page: the providers, a number per row to launch
// one, the selected one in detail beside them when there is room, and the
// actions that change them.
type providersPage struct {
	cursor    int
	filter    textinput.Model
	filtering bool
}

func newProvidersPage() *providersPage {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter by name, model, account or endpoint"
	return &providersPage{filter: ti}
}

func (p *providersPage) title() string { return "Launch" }

func (p *providersPage) capturing() bool { return p.filtering }

func (p *providersPage) consumesEsc() bool { return p.filter.Value() != "" }

func (p *providersPage) help() []string {
	if p.filtering {
		return []string{"enter keep filter", "esc clear"}
	}
	return []string{"enter/1-9 launch", "e edit", "a add", "d delete", "* default", "/ filter", "s sync"}
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
		hay := strings.ToLower(strings.Join([]string{n, pr.Display, pr.Model, pr.Account, pr.BaseURL, string(pr.Kind)}, " "))
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
	switch key.String() {
	case "a":
		return a.open(newFormOverlay(NewDraft("")))
	case "s":
		return a.sync()
	}
	if !has {
		return nil
	}

	switch key.String() {
	case "e":
		return a.open(newFormOverlay(EditDraft(a.cfg, name)))
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
	return p.launcherKeys(a, key.String())
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

// launchProvider launches name. A default variant launches straight away;
// otherwise it asks for a variant first when the provider has any.
func launchProvider(a *app, name string) tea.Cmd {
	if v := a.cfg.Providers[name].DefaultVariant; v != "" {
		return a.launch(Selection{Provider: name, Variant: v})
	}
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
	// The detail beside the list when there is room; below a narrow list,
	// only what would make the launch fail.
	if width >= 100 {
		// The detail is as wide as its longest lines need; the list keeps the rest.
		listW := width - min(max(width*2/5, 46), 60) - 2
		list := p.list(a, names, listW, height-lipgloss.Height(top))
		detail := detailPanel(a, name, width-listW-2)
		return top + lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(listW).Render(list), "  ", detail)
	}
	list := p.list(a, names, width, height-lipgloss.Height(top)-2)
	if pr := problemLine(a, name); pr != "" {
		list = strings.TrimRight(list, "\n") + "\n\n" + truncate(pr, width)
	}
	return top + list
}

func (p *providersPage) list(a *app, names []string, width, height int) string {
	cfg := a.cfg
	nameW, kindW, onW := len("PROVIDER"), len("ENGINE"), len("RUNS ON")
	for _, n := range names {
		nameW = max(nameW, lipgloss.Width(n))
		kindW = max(kindW, len(cfg.Providers[n].Kind))
		onW = max(onW, lipgloss.Width(runsOn(cfg, n)))
	}
	// The model takes what is left; runs-on gives way first on a narrow list.
	onW = min(onW, max(width/3, 12))
	modelW := max(width-nameW-kindW-onW-16, 8)

	var b strings.Builder
	head := rowNumber(-1) + fmt.Sprintf("    %-*s  %-*s  %-*s  %s", nameW, "PROVIDER", kindW, "ENGINE", onW, "RUNS ON", "MODEL")
	b.WriteString(headStyle.Render(head) + "\n")

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
		r.add(badgeStyle, rowNumber(i))
		st, mark := statusMark(a, n)
		r.add(st, mark)
		r.add(rowStyle, " "+pad(n, nameW)+"  "+pad(string(pr.Kind), kindW)+"  ")
		r.add(dimStyle, pad(truncate(runsOn(cfg, n), onW), onW)+"  ")
		model := truncate(modelCell(a, n), modelW)
		if model == "" {
			r.add(dimStyle, "-")
		} else {
			r.add(rowStyle, model)
		}
		if cfg.Settings.Default == n {
			r.add(badgeStyle, "  ★")
		}
		b.WriteString(r.render(width) + "\n")
	}
	if len(names) > start+rows {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more", len(names)-start-rows)))
	}
	return b.String()
}

// modelCell is what a provider answers with, in a list's width: the name a
// client asks when that is the model, else the model behind it and how many
// others would answer.
func modelCell(a *app, name string) string {
	p := a.cfg.Providers[name]
	models := core.ModelNames(a.cfg, name, a.routes.Models)
	if len(models) == 0 {
		return ""
	}
	rest := ""
	if len(models) > 1 {
		rest = fmt.Sprintf(" +%d", len(models)-1)
	}
	if p.Model == "" || p.Model == models[0] {
		return models[0] + rest
	}
	return p.Model + " → " + models[0] + rest
}

// saysMore reports whether the models a provider answers with say something
// its own model field does not.
func saysMore(models []string, model string) bool {
	return len(models) > 1 || (len(models) == 1 && models[0] != model)
}

// runsOn names what a provider talks to, short: its account, a pool's
// strategy, the provider pi knows, or its endpoint's host.
func runsOn(cfg *config.Config, name string) string {
	p := cfg.Providers[name]
	switch {
	case p.Account != "":
		return p.Account
	case p.IsPool():
		return "pool · " + p.StrategyOrDefault()
	case p.PiProvider != "":
		return "pi · " + p.PiProvider
	}
	return hostOf(p.BaseURL)
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

// detailPanel shows one provider at a glance: what it runs on and how it
// signs in, its models, what is left of its plan when that has been read,
// and its recent usage. A field with nothing to say is left out.
func detailPanel(a *app, name string, width int) string {
	cfg := a.cfg
	p := cfg.Providers[name]
	inner := max(width-4, 10)

	var b strings.Builder
	title := titleStyle.Render(name)
	if cfg.Settings.Default == name {
		title += badgeStyle.Render("  ★ default")
	}
	b.WriteString(title + "\n")
	sub := string(p.Kind) + " · " + cfg.Settings.Prefix + name
	if p.Display != "" && p.Display != name {
		sub = p.Display + " · " + sub
	}
	b.WriteString(dimStyle.Render(truncate(sub, inner)) + "\n\n")

	// Each block is its fields; an empty block takes no room, and the
	// blocks are a blank line apart.
	var blocks []string
	var cur strings.Builder
	field := func(label, value string) {
		if value != "" {
			cur.WriteString(labelStyle.Render(pad(label, detailLabelW)) + truncate(value, inner-detailLabelW) + "\n")
		}
	}
	block := func(text string) {
		if text != "" {
			blocks = append(blocks, strings.TrimRight(text, "\n"))
		}
	}
	flush := func() { block(cur.String()); cur.Reset() }

	if p.IsPool() {
		field("members", strings.Join(p.Members, ", "))
		field("strategy", p.StrategyOrDefault())
		field("mapping", mappingSummary(p))
	} else {
		if p.Account != "" {
			field("account", accountSummary(cfg, p.Account))
		}
		if p.PiProvider == "" {
			field("endpoint", p.BaseURL)
		}
	}
	field("sign-in", authSummary(cfg, p))
	flush()

	field("model", p.Model)
	if models := core.ModelNames(cfg, name, a.routes.Models); saysMore(models, p.Model) {
		field("serves", strings.Join(models, ", "))
	}
	switch p.Kind {
	case config.KindClaude:
		if p.Opus != p.Model || p.Sonnet != p.Model || p.Haiku != p.Model {
			field("tiers", tiersSummary(p))
		}
	case config.KindCodex:
		field("wire api", p.WireAPI)
		field("effort", p.Reasoning)
	case config.KindPi:
		if p.PiProvider == "" && !p.IsPool() {
			field("pi api", cmp.Or(p.PiAPI, "anthropic-messages"))
		}
	}
	field("launch", launchSummary(cfg, name))
	flush()

	block(planSummary(a, name, inner))
	block(usageSummary(a, name))
	if p.IsPool() {
		block(poolFlow(a, name, inner))
	}
	if st, ok := a.statuses[name]; ok && !st.OK() {
		for _, pr := range st.Problems() {
			cur.WriteString(errStyle.Render("✗ "+pr) + "\n")
		}
		flush()
	}
	b.WriteString(strings.Join(blocks, "\n\n"))
	return panelStyle.Width(width - 2).Render(b.String())
}

// detailLabelW is the width of the detail panel's labels.
const detailLabelW = 10

// authSummary says how a provider signs in. Only a provider that needs a
// key and has none says it is missing.
func authSummary(cfg *config.Config, p config.Provider) string {
	switch {
	case p.IsPool():
		return "each member's own"
	case p.PiProvider != "":
		return "pi's own sign-in"
	case p.Account != "":
		if !cfg.Accounts[p.Account].HasKey() {
			return errStyle.Render("the account has no key")
		}
		return "the account's key"
	}
	return keySummary(p)
}

// tiersSummary lists the claude model tiers.
func tiersSummary(p config.Provider) string {
	return fmt.Sprintf("opus %s · sonnet %s · haiku %s", dash(p.Opus), dash(p.Sonnet), dash(p.Haiku))
}

// launchSummary is what a bare launch picks: the default variant, or that
// it asks among the variants.
func launchSummary(cfg *config.Config, name string) string {
	if v := cfg.Providers[name].DefaultVariant; v != "" {
		return "always " + v
	}
	if vs := variantNames(cfg, name); len(vs) > 0 {
		return "asks: " + strings.Join(vs, " ")
	}
	return ""
}

// planSummary is what is left of the provider's account or own plan, once
// the Accounts page has read it this run.
func planSummary(a *app, name string, width int) string {
	q, ok := a.quotaOf(name)
	if !ok || q.Error != "" || q.NoSource {
		return ""
	}
	return planBars(q, width)
}

// accountSummary names an account and its vendor.
func accountSummary(cfg *config.Config, name string) string {
	acc, ok := cfg.Accounts[name]
	if !ok {
		return errStyle.Render(name + " (missing)")
	}
	s := name
	if v, ok := vendor.Lookup(acc.Vendor); ok {
		s += " · " + v.Name
	}
	if acc.User != "" {
		s += " · " + acc.User
	}
	return s
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
