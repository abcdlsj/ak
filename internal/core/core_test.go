package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
)

func TestRenameRewritesPoolMembers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://b", APIKey: "k"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"a", "b"}, MemberModels: map[string]string{"a": "m1"}}

	if err := Rename(cfg, "a", "z"); err != nil {
		t.Fatal(err)
	}
	pool := cfg.Providers["pool"]
	if len(pool.Members) != 2 || pool.Members[0] != "z" {
		t.Fatalf("members = %v, want [z b]", pool.Members)
	}
	if pool.MemberModels["z"] != "m1" {
		t.Fatalf("mapping not carried over: %v", pool.MemberModels)
	}
	if _, ok := pool.MemberModels["a"]; ok {
		t.Fatalf("stale mapping left: %v", pool.MemberModels)
	}
}

func TestRemoveDropsPoolMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://b", APIKey: "k"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"a", "b"}, MemberModels: map[string]string{"a": "m1"}}

	if err := Remove(cfg, "a"); err != nil {
		t.Fatal(err)
	}
	pool := cfg.Providers["pool"]
	if len(pool.Members) != 1 || pool.Members[0] != "b" {
		t.Fatalf("members = %v, want [b]", pool.Members)
	}
	if len(pool.MemberModels) != 0 {
		t.Fatalf("mapping not dropped: %v", pool.MemberModels)
	}
}

func TestRemoveRefusesOnlyMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["solo"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"solo"}}

	if err := Remove(cfg, "solo"); err == nil {
		t.Fatal("removed a pool's only member")
	}
	if _, ok := cfg.Providers["solo"]; !ok {
		t.Fatal("the refused removal still changed the config")
	}
}

func TestRenameRejectedLeavesConfigIntact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://b", APIKey: "k"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"a", "b"}}

	if err := Rename(cfg, "a", "b"); err == nil {
		t.Fatal("rename onto an existing provider succeeded")
	}
	if got := cfg.Providers["pool"].Members; len(got) != 2 || got[0] != "a" {
		t.Fatalf("a rejected rename changed the pool: %v", got)
	}
}

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
