// Package ui 是 ak 的 TUI:Providers 与 Usage 两页。
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
	tea "github.com/charmbracelet/bubbletea"
)

// Run 启动 TUI。返回用户选择启动的供应商名,空表示只是退出。
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

// RunUI 是 cli 调用的入口:进 TUI,用户选了供应商就 exec 它。
func RunUI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		fmt.Println("还没有配置供应商。先 `ak add` 或 `ak import --from claude-settings`。")
		return nil
	}
	selected, err := Run(cfg)
	if err != nil {
		return err
	}
	if selected == "" {
		return nil
	}
	// 启动选中的供应商。用 syscall.Exec 让 claude 接管当前终端和进程,
	// 不留下一个多余的中间进程。
	bin := filepath.Join(config.ExpandHome(cfg.Settings.BinDir),
		cfg.Settings.Prefix+selected)
	argv := append([]string{bin}, os.Args[1:]...)
	return syscall.Exec(bin, argv, os.Environ())
}

// page 是 TUI 的两个页签。
type page int

const (
	pageProviders page = iota
	pageUsage
)

// Model 是 TUI 状态。
type Model struct {
	cfg      *config.Config
	page     page
	width    int
	height   int
	cursor   int
	usage    usage.Summary
	usageErr error
	quitting bool
	// selected 非空表示用户选了一个供应商要启动。
	selected string
}

// Selected 返回用户选择启动的供应商名,空表示只是退出。
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
				m.page = pageUsage
				return m, m.loadUsage()
			}
			m.page = pageProviders
			return m, nil

		case "left", "h":
			m.page = pageProviders
			return m, nil

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
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	header := "ak  ·  Providers / Usage(Tab 切换)  ·  q 退出\n\n"

	var body string
	switch m.page {
	case pageProviders:
		body = m.providersView()
	case pageUsage:
		body = m.usageView()
	}
	return header + body
}

func (m Model) providersView() string {
	names := m.cfg.Names()
	if len(names) == 0 {
		return "  没有供应商\n"
	}
	out := ""
	for i, n := range names {
		p := m.cfg.Providers[n]
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}
		def := ""
		if m.cfg.Settings.Default == n {
			def = "  (默认)"
		}
		out += fmt.Sprintf("%s%-16s %-7s %s%s\n",
			cursor, m.cfg.Settings.Prefix+n, p.Kind, dash(p.Model), def)
	}
	out += "\n回车启动选中的供应商。\n"
	return out
}

func (m Model) usageView() string {
	if m.usageErr != nil {
		return fmt.Sprintf("  读取用量失败:%v\n", m.usageErr)
	}
	if m.usage.TotalTokens == 0 {
		return "  正在统计,或本机还没有用量记录。\n"
	}
	out := fmt.Sprintf("总计  %s tokens\n\n", humanize(m.usage.TotalTokens))
	out += "按供应商:\n"
	for _, r := range m.usage.ByProvider {
		out += fmt.Sprintf("  %-16s %12s tokens\n", r.Name, humanize(r.Tokens))
	}
	return out
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
