// Package ui is ak's TUI: a home page that launches and edits providers, the
// Accounts and Usage pages a key away from it, and modal overlays (forms,
// sign-ins, confirmations, pickers).
package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/bind"
	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/probe"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Selection is what the user chose to launch.
type Selection struct {
	Provider string
	Variant  string
	SignIn   bool
}

// RunUI opens the TUI on the provider focus and returns what the user picked
// to launch; an empty Provider means nothing was.
func RunUI(cfg *config.Config, focus string) (Selection, error) {
	a := newApp(cfg)
	if focus != "" {
		a.home().focus(cfg, focus)
	}
	final, err := tea.NewProgram(a, tea.WithAltScreen()).Run()
	if err != nil {
		return Selection{}, err
	}
	if a, ok := final.(*app); ok {
		return a.selection, nil
	}
	return Selection{}, nil
}

// page is one screen of the app.
type page interface {
	// title names the page's tab in the header.
	title() string
	update(a *app, msg tea.Msg) tea.Cmd
	view(a *app, width, height int) string
	// help lists the page's key hints for the footer.
	help() []string
	// capturing reports whether the page is taking raw text input, which
	// suspends the global keys.
	capturing() bool
	// consumesEsc reports whether esc clears something on the page rather
	// than going back home.
	consumesEsc() bool
}

// The pages are tabs in this order; the launcher opens first.
const (
	pageHome = iota
	pageAccounts
	pageUsage
)

// overlay is a modal on top of the active page.
type overlay interface {
	// update returns done=true when the overlay should close.
	update(a *app, msg tea.Msg) (done bool, cmd tea.Cmd)
	view(a *app, width int) string
}

// usageState is the usage data shared by every page.
type usageState struct {
	rows    []usage.Row
	agy     map[int][]usage.AgyActivity
	err     error
	loading bool
	loaded  bool
}

type checkState struct {
	checking bool
	res      probe.Result
	has      bool
}

type app struct {
	cfg      *config.Config
	pages    []page
	active   int
	overlay  overlay
	statuses map[string]core.Status
	checks   map[string]checkState
	wd       string
	bound    map[config.Kind]string
	// routes is what each remote service said its names answer with, so a
	// provider asking a route shows the models behind it.
	routes  core.RemoteModels
	usage   usageState
	quotas  quotaStore
	spinner spinner.Model
	flash   flash

	// poolStats is the local gateway's flow view, refreshed while the home
	// page shows a pool.
	poolStats    map[string]gateway.PoolStats
	poolStatsErr error

	width, height int
	selection     Selection
	quitting      bool
}

// flash is a transient status line message.
type flash struct {
	text string
	err  bool
	at   time.Time
}

type (
	usageLoadedMsg struct {
		rows []usage.Row
		agy  map[int][]usage.AgyActivity
		err  error
	}
	usageRefinedMsg struct {
		rows []usage.Row
	}
	statusesMsg        map[string]core.Status
	providerCheckedMsg struct {
		name string
		res  probe.Result
	}
	routesMsg struct {
		routes core.RemoteModels
		err    error
	}
	flashMsg     flash
	flashTickMsg struct{}
	poolStatsMsg struct {
		stats gateway.Stats
		err   error
	}
	poolStatsTickMsg struct{}
)

const flashTTL = 4 * time.Second

func newApp(cfg *config.Config) *app {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(accentColor)
	home := newProvidersPage()
	// Open on the default provider so enter alone launches it.
	home.focus(cfg, cfg.Settings.Default)
	wd, _ := os.Getwd()
	bound := map[config.Kind]string{}
	if wd != "" {
		if store, err := bind.Default(); err == nil {
			for _, k := range config.Kinds {
				if b, ok, err := store.Lookup(wd, string(k)); err == nil && ok {
					bound[k] = b.Target
				}
			}
		}
	}
	return &app{
		cfg:     cfg,
		routes:  core.RemoteRoutes(cfg),
		pages:   []page{home, newAccountsPage(), newUsagePage()},
		checks:  map[string]checkState{},
		wd:      wd,
		bound:   bound,
		spinner: sp,
	}
}

// home is the providers page.
func (a *app) home() *providersPage { return a.pages[pageHome].(*providersPage) }

func (a *app) Init() tea.Cmd {
	cmds := []tea.Cmd{a.loadUsage(), a.loadStatuses(), a.spinner.Tick, a.poolStatsTick(), a.refreshRoutes()}
	// With nothing configured, start by adding a provider: preset, key, done.
	if len(a.cfg.Providers) == 0 {
		cmds = append(cmds, a.open(newFormOverlay(NewDraft(""))))
	}
	return tea.Batch(cmds...)
}

// loadUsage rescans the logs; after the first scan only new bytes are read.
func (a *app) loadUsage() tea.Cmd {
	if a.usage.loading {
		return nil
	}
	a.usage.loading = true
	cfg := a.cfg
	return func() tea.Msg {
		rows, err := usage.Load(cfg)
		agy := map[int][]usage.AgyActivity{}
		if err == nil {
			for _, d := range []int{7, 30, 90, 0} {
				agy[d] = usage.AgyActivities(cfg, usage.Filter{Since: usage.SinceDays(d)})
			}
		}
		return usageLoadedMsg{rows: rows, agy: agy, err: err}
	}
}

func hasRemoteAccounts(cfg *config.Config) bool {
	for _, a := range cfg.Accounts {
		if v, ok := vendor.Lookup(a.Vendor); ok && v.Quota == quota.RemoteSource {
			return true
		}
	}
	return false
}

// refineUsage asks remote ak services what their routes served so route tokens
// can be attributed and priced by model without blocking initial log loading.
func (a *app) refineUsage() tea.Cmd {
	if !hasRemoteAccounts(a.cfg) {
		return nil
	}
	cfg := a.cfg
	rows := a.usage.rows
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		served, _ := usage.FetchRemoteUsage(ctx, cfg, 0)
		if len(served) == 0 {
			return nil
		}
		return usageRefinedMsg{rows: usage.Refine(rows, served, cfg)}
	}
}

// checkProvider sends a minimal live probe to provider name (or its pool members).
func (a *app) checkProvider(name string) tea.Cmd {
	p, ok := a.cfg.Providers[name]
	if !ok {
		return nil
	}
	prev := a.checks[name]
	if prev.checking {
		return nil
	}
	prev.checking = true
	a.checks[name] = prev
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res := runCheck(ctx, cfg, name, p)
		return providerCheckedMsg{name: name, res: res}
	}
}

func runCheck(ctx context.Context, cfg *config.Config, name string, p config.Provider) probe.Result {
	resolver := secrets.Default()
	if !p.IsPool() {
		key, err := resolver.Resolve(p)
		if err != nil {
			return probe.Result{Provider: name, Status: probe.StatusFail, Detail: "key: " + err.Error()}
		}
		r := probe.Check(ctx, p, key)
		r.Provider = name
		return r
	}
	leaves := cfg.Leaves(name)
	members := make([]probe.Result, len(leaves))
	var wg sync.WaitGroup
	for i, m := range leaves {
		wg.Add(1)
		go func(i int, m string) {
			defer wg.Done()
			mp := cfg.Providers[m]
			key, err := resolver.Resolve(mp)
			if err != nil {
				members[i] = probe.Result{Provider: m, Status: probe.StatusFail, Detail: "key: " + err.Error()}
				return
			}
			r := probe.Check(ctx, mp, key)
			r.Provider = m
			members[i] = r
		}(i, m)
	}
	wg.Wait()
	return probe.Aggregate(name, members)
}

// hasPools reports whether any provider is a pool.
func (a *app) hasPools() bool {
	for _, p := range a.cfg.Providers {
		if p.IsPool() {
			return true
		}
	}
	return false
}

// poolStatsWanted is whether the flow view should be kept fresh: a pool exists
// and the home page is showing.
func (a *app) poolStatsWanted() bool { return a.hasPools() && a.active == pageHome }

// poolStatsTick keeps a poll running; each tick reloads only when wanted.
func (a *app) poolStatsTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return poolStatsTickMsg{} })
}

// loadPoolStats reads the gateway's flow view.
func (a *app) loadPoolStats() tea.Cmd {
	base := a.cfg.Settings.GatewayURL()
	return func() tea.Msg {
		st, err := gateway.FetchStats(base)
		return poolStatsMsg{stats: st, err: err}
	}
}

// refreshRoutes asks the remote services what their names answer with, when
// what is known is old enough to be worth asking again.
func (a *app) refreshRoutes() tea.Cmd {
	cfg, known := a.cfg, a.routes
	if !known.Stale() {
		return nil
	}
	return func() tea.Msg {
		routes, err := core.FetchRemoteRoutes(context.Background(), cfg)
		return routesMsg{routes: routes, err: err}
	}
}

func (a *app) loadStatuses() tea.Cmd {
	cfg := a.cfg
	return func() tea.Msg { return statusesMsg(core.Statuses(cfg)) }
}

func (a *app) notify(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return flashMsg{text: text, err: isErr, at: time.Now()} }
}

// sync regenerates the commands after a config change and refreshes health.
func (a *app) sync() tea.Cmd {
	cfg := a.cfg
	return tea.Sequence(
		func() tea.Msg {
			rep, err := core.Sync(cfg, false)
			if err != nil {
				return flashMsg{text: "sync failed: " + err.Error(), err: true, at: time.Now()}
			}
			c := rep.Counts()
			return flashMsg{text: fmt.Sprintf("synced: %d created, %d updated, %d removed",
				c[shim.ActionCreated], c[shim.ActionUpdated], c[shim.ActionRemoved]), at: time.Now()}
		},
		a.loadStatuses(),
	)
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case usageLoadedMsg:
		a.usage = usageState{rows: msg.rows, agy: msg.agy, err: msg.err, loaded: true}
		if msg.err == nil {
			return a, a.refineUsage()
		}
		return a, nil
	case usageRefinedMsg:
		if a.usage.loaded && !a.usage.loading && len(msg.rows) > 0 {
			a.usage.rows = msg.rows
		}
		return a, nil
	case providerCheckedMsg:
		a.checks[msg.name] = checkState{res: msg.res, has: true}
		switch msg.res.Status {
		case probe.StatusOK, probe.StatusSlow:
			return a, a.notify(fmt.Sprintf("%s: %s (%dms)", msg.name, msg.res.Status, msg.res.LatencyMS), false)
		case probe.StatusSkip:
			return a, a.notify(fmt.Sprintf("%s: %s", msg.name, msg.res.Detail), false)
		default:
			return a, a.notify(fmt.Sprintf("%s: %s (%s)", msg.name, msg.res.Status, out.Reason(msg.res.Detail)), true)
		}
	case statusesMsg:
		a.statuses = msg
		return a, nil
	case routesMsg:
		if msg.err == nil {
			a.routes = msg.routes
		}
		return a, nil
	case quotaReadMsg:
		a.quotas.got(msg)
		return a, nil
	case poolStatsMsg:
		a.poolStats, a.poolStatsErr = msg.stats.Pools, msg.err
		return a, nil
	case poolStatsTickMsg:
		cmds := []tea.Cmd{a.poolStatsTick()}
		if a.poolStatsWanted() {
			cmds = append(cmds, a.loadPoolStats())
		}
		return a, tea.Batch(cmds...)
	case flashMsg:
		a.flash = flash(msg)
		return a, tea.Tick(flashTTL, func(time.Time) tea.Msg { return flashTickMsg{} })
	case flashTickMsg:
		if time.Since(a.flash.at) >= flashTTL {
			a.flash = flash{}
		}
		return a, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		a.spinner, cmd = a.spinner.Update(msg)
		return a, cmd
	}

	if o := a.overlay; o != nil {
		done, cmd := o.update(a, msg)
		// An overlay that opened another in its place is not the one to close.
		if done && a.overlay == o {
			a.overlay = nil
		}
		return a, cmd
	}

	if key, ok := msg.(tea.KeyMsg); ok && !a.pages[a.active].capturing() {
		switch key.String() {
		case "ctrl+c", "q":
			return a.quit()
		case "?":
			return a, a.open(newHelpOverlay(a.active))
		case "tab":
			return a, a.show((a.active + 1) % len(a.pages))
		case "shift+tab":
			return a, a.show((a.active + len(a.pages) - 1) % len(a.pages))
		case "u":
			return a, a.show(pageUsage)
		case "esc":
			if a.active != pageHome && !a.pages[a.active].consumesEsc() {
				a.active = pageHome
				return a, nil
			}
		}
	}
	return a, a.pages[a.active].update(a, msg)
}

// show switches to page i, running what the page wants on being shown.
func (a *app) show(i int) tea.Cmd {
	a.active = i
	if s, ok := a.pages[i].(interface{ shown(*app) tea.Cmd }); ok {
		return s.shown(a)
	}
	return nil
}

func (a *app) quit() (tea.Model, tea.Cmd) {
	a.quitting = true
	return a, tea.Quit
}

// launch ends the program with a selection for RunUI to exec.
func (a *app) launch(sel Selection) tea.Cmd {
	a.selection = sel
	a.quitting = true
	return tea.Quit
}

func (a *app) View() string {
	if a.quitting {
		return ""
	}
	w := a.contentWidth()
	header := a.header(w)
	footer := a.footer(w)
	bodyH := a.height - appStyle.GetVerticalPadding() - lipgloss.Height(header) - lipgloss.Height(footer) - 2

	body := a.pages[a.active].view(a, w, bodyH)
	if a.overlay != nil {
		body = a.overlay.view(a, w)
	}
	// Pin the footer to the bottom so it does not jump as the body changes.
	if bodyH > 0 {
		body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	}
	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", footer))
}

// header shows the product name and the page tabs, the active one filled,
// with the current directory on the right and a rule under them.
func (a *app) header(width int) string {
	row := titleStyle.Render("ak") + " "
	for i, p := range a.pages {
		style := chipStyle
		if i == a.active {
			style = chipActiveStyle
		}
		row += " " + style.Render(p.title())
	}
	if a.usage.loading && a.active != pageHome {
		row += "  " + a.spinner.View() + dimStyle.Render(" scanning logs")
	}
	if a.wd != "" {
		dir := shortHome(a.wd)
		avail := width - lipgloss.Width(row) - 2
		if avail >= 10 {
			right := dimStyle.Render(truncate(dir, avail))
			row = spread(row, right, width)
		}
	}
	return row + "\n" + ruleStyle.Render(strings.Repeat("─", max(width, 1)))
}

// footer is the flash message if any, else the key hints that apply now.
func (a *app) footer(width int) string {
	if a.flash.text != "" {
		style := okStyle
		if a.flash.err {
			style = errStyle
		}
		return style.Render(truncate(a.flash.text, width))
	}
	var keys []string
	if a.overlay == nil {
		keys = a.pages[a.active].help()
		keys = append(keys, "? help", "tab switch", "q quit")
	}
	return dimStyle.Render(truncate(strings.Join(keys, "  ·  "), width))
}

// contentWidth is the space inside the app padding.
//
// WindowSizeMsg has not arrived on the very first frame; fall back to a
// conservative width rather than assuming an arbitrarily wide terminal.
func (a *app) contentWidth() int {
	const fallback = 96
	w := a.width
	if w <= 0 {
		w = fallback
	}
	return max(w-appStyle.GetHorizontalPadding(), 20)
}
