package provider

import (
	"encoding/json"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// A settings layer is passed as --settings JSON; config_dir as CLAUDE_CONFIG_DIR.
func TestClaudeIsolation(t *testing.T) {
	p := config.Provider{Kind: config.KindClaude, BaseURL: "https://a", Model: "m", ConfigDir: "/tmp/cfg",
		Settings: map[string]any{"effortLevel": "low", "statusLine": map[string]any{"type": "command", "command": "x"}}}
	l, err := claudeEngine{}.Launch("a", p, Literal("k"), Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Args) != 2 || l.Args[0] != "--settings" {
		t.Fatalf("args = %q", l.Args)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(l.Args[1]), &got); err != nil {
		t.Fatalf("--settings is not JSON: %v", err)
	}
	if got["effortLevel"] != "low" {
		t.Errorf("settings = %v", got)
	}
	// Stable across runs, so sync does not rewrite the command.
	l2, _ := claudeEngine{}.Launch("a", p, Literal("k"), Context{})
	if l2.Args[1] != l.Args[1] {
		t.Error("--settings JSON is not stable")
	}
	if envMap(l.Env)["CLAUDE_CONFIG_DIR"] != "/tmp/cfg" {
		t.Error("CLAUDE_CONFIG_DIR not exported")
	}

	p.Settings = nil
	l, _ = claudeEngine{}.Launch("a", p, Literal("k"), Context{})
	if len(l.Args) != 0 {
		t.Errorf("args without settings = %q", l.Args)
	}
}
