package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/probe"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/usage"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Providers["alpha"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", Model: "m", Opus: "big"}
	cfg.Providers["beta"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://b"}
	return cfg
}

func press(a *app, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		a.Update(msg)
	}
}

func TestLaunchWithVariant(t *testing.T) {
	a := newApp(testConfig())
	// beta, then the picker: default, minimal, low.
	press(a, "down", "enter", "down", "down", "enter")
	if got := a.selection; got.Provider != "beta" || got.Variant != "low" {
		t.Fatalf("selection = %+v, want beta/low", got)
	}
}

func TestPickerEscReturnsToList(t *testing.T) {
	a := newApp(testConfig())
	press(a, "enter", "esc")
	if a.overlay != nil || a.selection.Provider != "" || a.quitting {
		t.Fatalf("esc should close the picker only: overlay=%v selection=%+v", a.overlay, a.selection)
	}
}

// While typing a filter, q is text, not quit.
func TestFilterCapturesKeys(t *testing.T) {
	a := newApp(testConfig())
	press(a, "/", "q")
	if a.quitting {
		t.Fatal("q quit the app while filtering")
	}
	p := a.pages[0].(*providersPage)
	if p.filter.Value() != "q" {
		t.Fatalf("filter = %q", p.filter.Value())
	}
	press(a, "esc", "b", "enter")
	// esc cleared the filter; b is not bound, enter launches the first
	// provider's picker.
	if _, ok := a.overlay.(*pickOverlay); !ok {
		t.Fatalf("overlay = %T, want the variant picker", a.overlay)
	}
}

// A digit on the home page launches that row directly.
func TestDigitLaunches(t *testing.T) {
	cfg := testConfig()
	cfg.Providers["gamma"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://g"}
	a := newApp(cfg)
	press(a, "3")
	if a.selection.Provider != "gamma" || !a.quitting {
		t.Fatalf("selection = %+v, want gamma", a.selection)
	}
}

// Home opens on the default provider, so enter alone launches it.
func TestHomeFocusesDefault(t *testing.T) {
	cfg := testConfig()
	cfg.Providers["gamma"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://g"}
	cfg.Settings.Default = "gamma"
	a := newApp(cfg)
	press(a, "enter")
	if a.selection.Provider != "gamma" {
		t.Fatalf("selection = %+v, want gamma", a.selection)
	}
}

// esc on home first clears the filter; on another page it goes home.
func TestEscClearsThenGoesHome(t *testing.T) {
	a := newApp(testConfig())
	press(a, "/", "x", "enter")
	if !a.home().consumesEsc() {
		t.Fatal("the filter should be kept")
	}
	press(a, "esc")
	if a.active != pageHome || a.home().consumesEsc() || a.quitting {
		t.Fatal("esc should clear the filter and stay home")
	}
	press(a, "u", "esc")
	if a.active != pageHome || a.quitting {
		t.Fatal("esc on usage should go home")
	}
}

// tab and shift+tab cycle the pages, wrapping at either end.
func TestTabCyclesPages(t *testing.T) {
	a := newApp(testConfig())
	for _, want := range []int{pageAccounts, pageUsage, pageHome} {
		press(a, "tab")
		if a.active != want {
			t.Fatalf("active = %d, want %d", a.active, want)
		}
	}
	press(a, "shift+tab")
	if a.active != pageUsage {
		t.Fatalf("active = %d, want usage", a.active)
	}
}

// On the home page e edits and enter still launches.
func TestHomeEditsWithE(t *testing.T) {
	a := newApp(testConfig())
	press(a, "e")
	if _, ok := a.overlay.(*formOverlay); !ok || a.quitting {
		t.Fatalf("overlay = %T, want the edit form", a.overlay)
	}
	press(a, "esc", "enter")
	if o, ok := a.overlay.(*pickOverlay); !ok || o.provider != "alpha" {
		t.Fatalf("overlay = %T, want the variant picker for alpha", a.overlay)
	}
}

// With nothing configured, a on the home page adds without a detour.
func TestHomeAddWhenEmpty(t *testing.T) {
	a := newApp(config.Default())
	press(a, "a")
	if _, ok := a.overlay.(*formOverlay); !ok {
		t.Fatalf("overlay = %T, want the add form", a.overlay)
	}
}

// ctrl+s saves the edit form from any field.
func TestFormCtrlSSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.DirEnv, t.TempDir())
	a := newApp(testConfig())
	press(a, "e")
	if _, ok := a.overlay.(*formOverlay); !ok {
		t.Fatalf("overlay = %T, want the form", a.overlay)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if a.overlay != nil {
		t.Fatalf("ctrl+s left the form open")
	}
}

// A provider with a default variant launches straight away, without the picker.
func TestDefaultVariantSkipsPicker(t *testing.T) {
	cfg := testConfig()
	p := cfg.Providers["beta"]
	p.DefaultVariant = "low"
	cfg.Providers["beta"] = p
	a := newApp(cfg)
	press(a, "down", "enter")
	if a.overlay != nil || a.selection.Provider != "beta" || a.selection.Variant != "low" || !a.quitting {
		t.Fatalf("overlay=%v selection=%+v quitting=%v", a.overlay, a.selection, a.quitting)
	}
}

// With nothing configured, the launcher opens on the add form.
func TestOnboardingOpensAddForm(t *testing.T) {
	a := newApp(config.Default())
	a.Init()
	if _, ok := a.overlay.(*formOverlay); !ok {
		t.Fatalf("overlay = %T, want the add form", a.overlay)
	}
	b := newApp(testConfig())
	b.Init()
	if b.overlay != nil {
		t.Fatalf("overlay = %T with providers configured", b.overlay)
	}
}

func TestBoardShowsRemoteAccounts(t *testing.T) {
	s := quotaStore{cards: []*card{
		{key: "account:kimi", title: "kimi", read: true},
		{key: "account:home", title: "home", read: true, q: quota.Quota{Accounts: []quota.Quota{
			{Provider: "home/a", Windows: []quota.Window{{Name: "5h", Used: 10}}}, {Provider: "home/b"},
		}}},
	}}
	var titles []string
	for _, c := range s.shown() {
		titles = append(titles, c.title)
	}
	if strings.Join(titles, ",") != "kimi,home/a,home/b" {
		t.Errorf("cards = %v", titles)
	}
}

func TestHelpOverlayToggle(t *testing.T) {
	a := newApp(testConfig())
	press(a, "?")
	o, ok := a.overlay.(*helpOverlay)
	if !ok {
		t.Fatalf("overlay = %T, want *helpOverlay", a.overlay)
	}
	view := ansi.Strip(o.view(a, 90))
	if !strings.Contains(view, "Launch shortcuts") || !strings.Contains(view, "live health check") {
		t.Errorf("help view = %s", view)
	}
	press(a, "?")
	if a.overlay != nil || a.quitting {
		t.Fatalf("second ? should close help: overlay=%v quitting=%v", a.overlay, a.quitting)
	}
}

func TestAntigravityUsageSummaryAndUsagePage(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["work"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/work"}
	a := newApp(cfg)
	a.Update(usageLoadedMsg{
		agy: map[int][]usage.AgyActivity{
			30: {{Provider: "work", Conversations: 4, Steps: 35, Last: time.Now()}},
			0:  {{Provider: "work", Conversations: 6, Steps: 50, Last: time.Now()}},
		},
	})
	if got := ansi.Strip(usageSummary(a, "work")); !strings.Contains(got, "4 conversations · 35 steps") {
		t.Errorf("usageSummary = %q", got)
	}
	press(a, "u")
	view := ansi.Strip(a.pages[pageUsage].view(a, 100, 30))
	if !strings.Contains(view, "Antigravity") || !strings.Contains(view, "6 conv") || !strings.Contains(view, "50 steps") {
		t.Errorf("usage page missing agy section:\n%s", view)
	}
}

func TestHomeCheckSignInAndBoundDetail(t *testing.T) {
	cfg := testConfig()
	cfg.Providers["cx-native"] = config.Provider{Kind: config.KindCodex, CodexHome: "/tmp/cx"}
	a := newApp(cfg)
	a.bound[config.KindClaude] = "alpha"
	a.Update(providerCheckedMsg{name: "alpha", res: probe.Result{Provider: "alpha", Status: probe.StatusOK, LatencyMS: 120}})
	detail := ansi.Strip(detailPanel(a, "alpha", 56, 14))
	if !strings.Contains(detail, "claude bound here") || !strings.Contains(detail, "ok (120ms)") {
		t.Errorf("detailPanel = \n%s", detail)
	}
	if h := lipgloss.Height(detail); h > 14 {
		t.Errorf("detailPanel height = %d, want <= 14", h)
	}

	// Select cx-native and press l -> SignIn selection.
	a.home().focus(cfg, "cx-native")
	press(a, "l")
	if !a.selection.SignIn || a.selection.Provider != "cx-native" || !a.quitting {
		t.Errorf("selection = %+v, quitting=%v", a.selection, a.quitting)
	}
}
