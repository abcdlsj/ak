package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A provider on an account loads with the vendor's endpoint and the
// account's key and secrets, and saves only what it overrides; the key and
// secrets go to auth.json under the account.
func TestAccountRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)
	cfg := Default()
	cfg.Accounts["od"] = Account{Vendor: "opendesign", APIKey: "sk-amr", Secrets: map[string]string{"control": "vela"}}
	cfg.Providers["od"] = Provider{Kind: KindCodex, Account: "od", Model: "glm-5.3-flash"}
	cfg.Resolve()
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(dir, "providers.toml"))
	for _, leaked := range []string{"base_url", "wire_api", "sk-amr", "vela"} {
		if strings.Contains(string(toml), leaked) {
			t.Errorf("providers.toml holds %s:\n%s", leaked, toml)
		}
	}
	auth, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	if !strings.Contains(string(auth), `"account:od:control": "vela"`) {
		t.Errorf("auth.json = %s", auth)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := got.Providers["od"]
	if p.BaseURL != "https://amr-link.open-design.ai/v1" || p.WireAPI != "responses" ||
		p.APIKey != "sk-amr" || p.Secrets["control"] != "vela" || p.Model != "glm-5.3-flash" {
		t.Errorf("loaded %+v", p)
	}
}

func TestAccountValidation(t *testing.T) {
	cfg := Default()
	cfg.Accounts["og"] = Account{Vendor: "opencode-go"}
	cfg.Providers["x"] = Provider{Kind: KindCodex, Account: "og"}
	cfg.Resolve()
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "does not serve") {
		t.Errorf("codex on OpenCode Go: %v", err)
	}
	cfg.Providers["x"] = Provider{Kind: KindClaude, Account: "nope"}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "unknown account") {
		t.Errorf("unknown account: %v", err)
	}
}
