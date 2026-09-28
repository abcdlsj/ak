package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
)

func TestRename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a"}
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://b"}
	cfg.Settings.Default = "cl"
	if err := usage.RecordSession("s1", "cl"); err != nil {
		t.Fatal(err)
	}

	if err := Rename(cfg, "cl", "cx"); err == nil {
		t.Fatal("rename onto an existing provider succeeded")
	}
	if err := Rename(cfg, "cl", "claude2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["cl"]; ok || cfg.Settings.Default != "claude2" {
		t.Fatalf("rename left %v, default %q", cfg.Names(), cfg.Settings.Default)
	}
	data, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local/share/ak/sessions.jsonl"))
	if !strings.Contains(string(data), `"provider":"claude2"`) {
		t.Fatalf("session records not rewritten: %s", data)
	}

	// A codex provider keeps its provider_id, so its history still maps.
	if err := Rename(cfg, "cx", "codex2"); err != nil {
		t.Fatal(err)
	}
	if id := cfg.Providers["codex2"].ProviderID; id != "cx" {
		t.Fatalf("provider_id = %q, want cx", id)
	}
}
