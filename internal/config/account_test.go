package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/vendor"
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

// An endpoint says its protocol once; the engines' own names for it follow,
// and are not written back as if the provider had chosen them.
func TestProtocolFillsTheEngineNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "relay.toml"), []byte(`id = "relay"
[[endpoints]]
protocol = "chat"
base_url = "https://relay.example/v1"
[[endpoints]]
protocol = "anthropic"
base_url = "https://relay.example"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	vendor.LoadDir(dir)
	t.Cleanup(func() { vendor.LoadDir("") })

	cfg := Default()
	cfg.Accounts["a"] = Account{Vendor: "relay"}
	cfg.Providers["codex"] = Provider{Kind: KindCodex, Account: "a", Model: "gpt"}
	cfg.Providers["pi"] = Provider{Kind: KindPi, Account: "a", Model: "gpt"}
	cfg.Providers["claude"] = Provider{Kind: KindClaude, Account: "a", Model: "claude"}

	if p, ok := cfg.OnAccountProtocol("a", "chat"); !ok || p.WireAPI != "chat" || p.PiAPI != "openai-completions" {
		t.Errorf("chat on the account = %+v, %v", p, ok)
	}
	cfg.Resolve()
	// Codex takes Responses when a vendor serves it, else the nearest
	// protocol it can be translated from; this one serves Chat.
	if p := cfg.Providers["codex"]; p.WireAPI != "chat" || p.BaseURL != "https://relay.example/v1" {
		t.Errorf("codex = %+v", p)
	}
	if p := cfg.Providers["claude"]; p.BaseURL != "https://relay.example" {
		t.Errorf("claude = %+v", p)
	}
	// Stored leaves out what the account gave.
	if p := cfg.Stored(cfg.Providers["codex"]); p.WireAPI != "" || p.BaseURL != "" {
		t.Errorf("stored codex = %+v", p)
	}
}
