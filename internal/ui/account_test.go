package ui

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
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
