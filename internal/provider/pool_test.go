package provider

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

const testGateway = "http://127.0.0.1:17877"

func TestClaudePoolLaunchPointsAtGateway(t *testing.T) {
	p := config.Provider{Kind: config.KindClaude, Members: []string{"a", "b"}, Model: "logical"}
	l, err := claudeEngine{}.Launch("pool", p, Literal("real-key-must-not-appear"), Context{Gateway: testGateway})
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(l.Env)
	if got := env["ANTHROPIC_BASE_URL"]; got != testGateway+"/p/pool" {
		t.Errorf("base url = %q, want the gateway", got)
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != poolKey {
		t.Errorf("auth token = %q, want the pool placeholder", got)
	}
	if got := env["ANTHROPIC_MODEL"]; got != "logical" {
		t.Errorf("model = %q, want logical", got)
	}
	for _, kv := range l.Env.Set {
		if strings.Contains(kv.Value, "real-key") {
			t.Errorf("%s leaked the member key into the command", kv.Key)
		}
	}
}

func TestCodexPoolLaunchPointsAtGateway(t *testing.T) {
	home := t.TempDir()
	p := config.Provider{Kind: config.KindCodex, Members: []string{"a"}, Model: "gpt-x"}
	l, err := codexEngine{}.Launch("pool", p, Literal("real-key-must-not-appear"), Context{Gateway: testGateway, CodexHome: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Files) != 0 {
		t.Errorf("pool wrote %d files, want none", len(l.Files))
	}
	args := strings.Join(l.Args, " ")
	if !strings.Contains(args, testGateway+"/p/pool") {
		t.Errorf("args do not point at the gateway:\n%s", args)
	}
	if !strings.Contains(args, "AK_KEY_POOL") {
		t.Errorf("args are missing the pool env key:\n%s", args)
	}
	if strings.Contains(args, "real-key") {
		t.Errorf("args leaked the member key:\n%s", args)
	}
	env := envMap(l.Env)
	if got := env["AK_KEY_POOL"]; got != poolKey {
		t.Errorf("AK_KEY_POOL = %q, want the pool placeholder", got)
	}
}

func TestNonPoolIgnoresGateway(t *testing.T) {
	p := config.Provider{Kind: config.KindClaude, BaseURL: "https://real.example", APIKey: "sk-real"}
	l, err := claudeEngine{}.Launch("plain", p, Literal("sk-real"), Context{Gateway: testGateway})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(l.Env)["ANTHROPIC_BASE_URL"]; got != "https://real.example" {
		t.Errorf("base url = %q, want the provider's own", got)
	}
}
