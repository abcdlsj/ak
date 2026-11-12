package ui

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	tea "github.com/charmbracelet/bubbletea"
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
