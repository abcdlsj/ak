// Package ui is ak's TUI: an app shell hosting pages (Providers, Usage) and
// modal overlays (forms, confirmations, pickers).
package ui

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Selection is what the user chose to launch.
type Selection struct {
	Provider string
	Variant  string
}

// RunUI opens the TUI, then execs the provider the user picked.
func RunUI() error {
	cfg, err := core.Load()
	if err != nil {
		return err
	}
	final, err := tea.NewProgram(newApp(cfg), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	a, ok := final.(*app)
	if !ok || a.selection.Provider == "" {
		return nil
	}
	// syscall.Exec lets the engine take over the terminal and process,
	// leaving no intermediate process behind.
	bin := core.CommandPath(a.cfg, a.selection.Provider)
	argv := []string{bin}
	if a.selection.Variant != "" {
		argv = append(argv, a.selection.Variant)
	}
	return syscall.Exec(bin, argv, os.Environ())
}

// page is one tab of the app.
type page interface {
	title() string
	update(a *app, msg tea.Msg) tea.Cmd
	view(a *app, width, height int) string
	// help lists the page's key hints for the footer.
	help() []string
	// capturing reports whether the page is taking raw text input, which
	// suspends the global keys.
	capturing() bool
}

// overlay is a modal on top of the active page.
type overlay interface {
	// update returns done=true when the overlay should close.
	update(a *app, msg tea.Msg) (done bool, cmd tea.Cmd)
	view(a *app, width int) string
}

// usageState is the usage data shared by every page.
type usageState struct {
	rows    []usage.Row
	err     error
	loading bool
	loaded  bool
}

type app struct {
	cfg      *config.Config
	pages    []page
	active   int
	overlay  overlay
	statuses map[string]core.Status
	usage    usageState
	spinner  spinner.Model
	flash    flash

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
		err  error
	}
	statusesMsg  map[string]core.Status
	flashMsg     flash
	flashTickMsg struct{}
)

const flashTTL = 4 * time.Second

func newApp(cfg *config.Config) *app {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(accentColor)
	return &app{
		cfg:     cfg,
		pages:   []page{newProvidersPage(), newUsagePage()},
		spinner: sp,
	}
}

func (a *app) Init() tea.Cmd {
	return tea.Batch(a.loadUsage(), a.loadStatuses(), a.spinner.Tick)
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
		return usageLoadedMsg{rows, err}
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
		a.usage = usageState{rows: msg.rows, err: msg.err, loaded: true}
		return a, nil
	case statusesMsg:
		a.statuses = msg
		return a, nil
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

	if a.overlay != nil {
		done, cmd := a.overlay.update(a, msg)
		if done {
			a.overlay = nil
		}
		return a, cmd
	}

	if key, ok := msg.(tea.KeyMsg); ok && !a.pages[a.active].capturing() {
		switch key.String() {
		case "ctrl+c", "q":
			return a.quit()
		case "tab":
			a.active = (a.active + 1) % len(a.pages)
			return a, nil
		case "shift+tab":
			a.active = (a.active + len(a.pages) - 1) % len(a.pages)
			return a, nil
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if i := int(key.String()[0] - '1'); i < len(a.pages) {
				a.active = i
			}
			return a, nil
		}
	}
	return a, a.pages[a.active].update(a, msg)
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
	header := a.tabBar(w)
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

// tabBar shows the product name and the pages, with a rule under them.
func (a *app) tabBar(width int) string {
	parts := []string{titleStyle.Render("ak"), "  "}
	for i, p := range a.pages {
		label := fmt.Sprintf("%d %s", i+1, p.title())
		if i == a.active {
			parts = append(parts, tabActiveStyle.Render(label))
		} else {
			parts = append(parts, tabInactiveStyle.Render(label))
		}
	}
	row := lipgloss.JoinHorizontal(lipgloss.Bottom, parts...)
	if a.usage.loading {
		row += "  " + a.spinner.View() + dimStyle.Render(" scanning logs")
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
		keys = append(a.pages[a.active].help(), "tab page", "q quit")
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
