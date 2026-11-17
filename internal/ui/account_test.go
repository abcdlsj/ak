package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/vendor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func accountConfig() *config.Config {
	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code", APIKey: "k"}
	cfg.Providers["goat"] = config.Provider{Kind: config.KindClaude, Account: "goat", Model: "claude-opus-5-5"}
	cfg.Resolve()
	return cfg
}

// Picking an account makes a provider that leaves its endpoint and key to
// the account, suggested a name after its vendor.
func TestDraftOnAccount(t *testing.T) {
	cfg := accountConfig()
	d := NewDraft("")
	d.Kind, d.choice = string(config.KindCodex), choiceAccount+"goat"
	d.ApplyPick(cfg)
	if _, ok := d.SignInFirst(); ok {
		t.Fatal("an account needs no sign-in")
	}
	p := d.Provider()
	if p.Account != "goat" || p.BaseURL != "" || p.APIKey != "" {
		t.Errorf("provider %+v", p)
	}
	if got := core.SuggestName(cfg, p); got != "commandcode-codex" {
		t.Errorf("suggested name %q, want commandcode-codex", got)
	}
	if d.defaults.Model == "" {
		t.Error("the form has no account default to show")
	}
}

// Editing starts from what providers.toml holds: an overridden field
// stays, a field the account gives is blank.
func TestEditDraftOnAccount(t *testing.T) {
	d := EditDraft(accountConfig(), "goat")
	if d.Model != "claude-opus-5-5" || d.BaseURL != "" || d.Key != "" {
		t.Errorf("draft %+v", d)
	}
	if p := d.Provider(); p.Account != "goat" || p.BaseURL != "" {
		t.Errorf("provider %+v", p)
	}
}

// Choosing a vendor signs in to it before the form.
func TestDraftSignInFirst(t *testing.T) {
	d := NewDraft("")
	d.choice = choiceVendor + "opendesign"
	d.ApplyPick(config.Default())
	if v, ok := d.SignInFirst(); !ok || v.ID != "opendesign" {
		t.Fatalf("SignInFirst = %v, %v", v.ID, ok)
	}
}

// A provider says it is missing a key only when it signs in with one.
func TestAuthSummary(t *testing.T) {
	cfg := accountConfig()
	cfg.Accounts["empty"] = config.Account{Vendor: "kimi-coding"}
	cases := map[string]config.Provider{
		"pi's own sign-in":       {Kind: config.KindPi, PiProvider: "commandcode"},
		"each member's own":      {Kind: config.KindClaude, Members: []string{"goat"}},
		"the account's key":      {Kind: config.KindClaude, Account: "goat"},
		"the account has no key": {Kind: config.KindClaude, Account: "empty"},
		"missing":                {Kind: config.KindClaude, BaseURL: "https://x"},
	}
	for want, p := range cases {
		if got := ansi.Strip(authSummary(cfg, p)); got != want {
			t.Errorf("%+v: %q, want %q", p, got, want)
		}
	}
}

func TestNativeAndAgySummaryAndDraft(t *testing.T) {
	agyDir := t.TempDir()
	raw := []byte(`{"token":{"access_token":"ya29.a","refresh_token":"1//r"},"id_token_email":"pro@gmail.com"}`)
	if err := provider.WriteAntigravityToken(agyDir, raw); err != nil {
		t.Fatal(err)
	}
	cxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cxDir, "auth.json"), []byte(`{"tokens":{"access_token":"a","refresh_token":"r"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyDir := t.TempDir()

	cfg := config.Default()
	cfg.Providers["agy-pro"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: agyDir}
	cfg.Providers["agy-new"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: emptyDir}
	cfg.Providers["cx-pro"] = config.Provider{Kind: config.KindCodex, CodexHome: cxDir}
	cfg.Providers["cx-new"] = config.Provider{Kind: config.KindCodex, CodexHome: emptyDir}
	cfg.Providers["cl-pro"] = config.Provider{Kind: config.KindClaude, ConfigDir: emptyDir}

	if got := ansi.Strip(authSummary(cfg, cfg.Providers["agy-pro"])); got != "agy · pro@gmail.com" {
		t.Errorf("agy-pro authSummary = %q", got)
	}
	if got := runsOn(cfg, "agy-pro"); got != "agy · pro@gmail.com" {
		t.Errorf("agy-pro runsOn = %q", got)
	}
	if got := ansi.Strip(authSummary(cfg, cfg.Providers["agy-new"])); got != "not signed in" {
		t.Errorf("agy-new authSummary = %q", got)
	}
	if got := ansi.Strip(authSummary(cfg, cfg.Providers["cx-pro"])); got != "codex's own sign-in" {
		t.Errorf("cx-pro authSummary = %q", got)
	}
	if got := ansi.Strip(authSummary(cfg, cfg.Providers["cx-new"])); got != "sign in on first launch" {
		t.Errorf("cx-new authSummary = %q", got)
	}
	if got := runsOn(cfg, "cl-pro"); got != "claude · native" {
		t.Errorf("cl-pro runsOn = %q", got)
	}

	d := NewDraft("work")
	d.Kind = string(config.KindAntigravity)
	d.choice = choiceNative
	d.ApplyPick(cfg)
	final := d.Finalize("work")
	if final.HomeDir() == "" || final.Kind != config.KindAntigravity {
		t.Fatalf("Finalize agy = %+v", final)
	}
}

func TestAccountsNewProviderAndPlanModelOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { vendor.LoadDir(t.TempDir()) })

	if err := os.MkdirAll(filepath.Join(dir, "vendors.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendors.d", "single.toml"),
		[]byte("id = \"single\"\nname = \"Single\"\n[engines.claude]\nbase_url = \"https://single.example\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Accounts["alpha"] = config.Account{Vendor: "single", APIKey: "k1"}
	cfg.Accounts["beta"] = config.Account{Vendor: "single", APIKey: "k2"}
	cfg.Resolve()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()

	a := newApp(cfg)
	a.quotas.reset(cfg)
	a.active = pageAccounts

	// end/G moves to beta, home/g back to alpha.
	press(a, "G")
	if c, ok := a.pages[pageAccounts].(*accountsPage).selected(a); !ok || c.title != "beta" {
		t.Fatalf("after G selected = %+v", c)
	}
	press(a, "g")
	if c, ok := a.pages[pageAccounts].(*accountsPage).selected(a); !ok || c.title != "alpha" {
		t.Fatalf("after g selected = %+v", c)
	}

	// Single-engine vendor skips PickForm when pressing n.
	press(a, "n")
	fo, ok := a.overlay.(*formOverlay)
	if !ok || fo.picking || fo.draft.Account != "alpha" || fo.draft.Kind != "claude" {
		t.Fatalf("n on single-engine = %+v (picking=%v)", fo, fo != nil && fo.picking)
	}
	press(a, "esc")

	// Pressing m opens accountModelOverlay and saves the model.
	press(a, "m")
	if _, ok := a.overlay.(*accountModelOverlay); !ok {
		t.Fatalf("overlay = %T, want *accountModelOverlay", a.overlay)
	}
	for _, r := range "claude-opus-5-5" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.overlay != nil {
		t.Fatalf("overlay still open: %T", a.overlay)
	}
	if cmd != nil {
		a.Update(cmd())
	}
	if got := a.cfg.Accounts["alpha"].Model; got != "claude-opus-5-5" {
		t.Errorf("account model = %q, want claude-opus-5-5", got)
	}
	if !strings.Contains(a.flash.text, "claude-opus-5-5") {
		t.Errorf("flash = %q", a.flash.text)
	}
}
