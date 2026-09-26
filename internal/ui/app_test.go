package ui

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
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

// m and u open the secondary pages; esc returns home, but first clears what
// the page itself holds.
func TestSecondaryPagesEscHome(t *testing.T) {
	a := newApp(testConfig())
	press(a, "m", "/", "x", "enter")
	if a.active != pageManage {
		t.Fatalf("active = %d, want manage", a.active)
	}
	press(a, "esc")
	if a.active != pageManage || a.pages[pageManage].consumesEsc() {
		t.Fatal("first esc should clear the manage filter")
	}
	press(a, "esc")
	if a.active != pageHome {
		t.Fatalf("active = %d, want home", a.active)
	}
	press(a, "u", "esc")
	if a.active != pageHome || a.quitting {
		t.Fatal("esc on usage should go home")
	}
}

// On the manage page enter edits; launching is the home page's job.
func TestManageEnterEdits(t *testing.T) {
	a := newApp(testConfig())
	press(a, "m", "enter")
	if _, ok := a.overlay.(*formOverlay); !ok || a.quitting {
		t.Fatalf("overlay = %T, want the edit form", a.overlay)
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
