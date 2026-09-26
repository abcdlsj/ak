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
