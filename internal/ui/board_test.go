package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestPixels(t *testing.T) {
	rows := pixels("10", noStyle)
	want := [pixelRows]string{"▄█  █▀█", " █  █ █", "▀▀▀ ▀▀▀"}
	if rows != want {
		t.Errorf("pixels(10) =\n%s\nwant\n%s", strings.Join(rows[:], "\n"), strings.Join(want[:], "\n"))
	}
}

func TestTicks(t *testing.T) {
	cases := map[float64]int{0: 0, 0.001: 1, 0.5: 5, 1: 10, 2: 10}
	for share, on := range cases {
		got := ansi.Strip(ticks(share, 10, noStyle, noStyle))
		if n := strings.Count(got, "▍"); n != 10 {
			t.Errorf("ticks(%v) has %d ticks, want 10", share, n)
		}
		if lit := strings.Count(ticks(share, 10, noStyle, noStyle)[:on*len("▍")], "▍"); lit != on {
			t.Errorf("ticks(%v) lit %d, want %d", share, lit, on)
		}
	}
}

// Every card fits its width, whatever it shows.
func TestBoardCardsFit(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code", APIKey: "k"}
	b := newBoard(cfg)
	b.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	soon := time.Now().Add(2*time.Hour + 14*time.Minute)
	bal, used, limit := 14.47, 5.53, 20.0
	b.Update(boardReadMsg{0, quota.Quota{Windows: []quota.Window{
		{Name: "5h", Used: 42, Resets: &soon}, {Name: "weekly", Used: 100},
	}, Balance: &bal, Used: &used, Limit: &limit, Currency: "USD"}})
	view := b.View()
	if os.Getenv("AK_BOARD_SHOW") != "" {
		t.Log("\n" + view)
	}
	for _, want := range []string{"goat", "Command Code", "5h", "in 2h 14m", "weekly", "Balance"} {
		if !strings.Contains(ansi.Strip(view), want) {
			t.Errorf("board lacks %q:\n%s", want, ansi.Strip(view))
		}
	}
	for _, line := range strings.Split(b.render(b.cards[0]), "\n") {
		if w := ansi.StringWidth(line); w != cardWidth {
			t.Fatalf("card line is %d wide, want %d: %q", w, cardWidth, ansi.Strip(line))
		}
	}
}

var noStyle = lipgloss.NewStyle()
