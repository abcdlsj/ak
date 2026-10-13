package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func readAuth(t *testing.T) map[string]string {
	t.Helper()
	path, _ := AuthPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Keys go to auth.json under auth_key; providers.toml only names the entry.
func TestSaveKeepsKeysOutOfToml(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := Default()
	cfg.Providers["kimi"] = Provider{Kind: KindClaude, BaseURL: "https://x", APIKey: "sk-secret"}
	cfg.Providers["ref"] = Provider{Kind: KindClaude, BaseURL: "https://x", APIKeyRef: "env:K"}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	toml, _ := os.ReadFile(path)
	if strings.Contains(string(toml), "sk-secret") || !strings.Contains(string(toml), `auth_key = 'kimi'`) {
		t.Fatalf("providers.toml:\n%s", toml)
	}
	authPath, _ := AuthPath()
	if fi, err := os.Stat(authPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json: %v %v", fi, err)
	}
	if got := readAuth(t); len(got) != 1 || got["kimi"] != "sk-secret" {
		t.Fatalf("auth.json = %v", got)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Providers["kimi"].APIKey != "sk-secret" || got.Providers["ref"].APIKey != "" {
		t.Fatalf("loaded %+v", got.Providers)
	}

	// A removed provider's entry is dropped on the next save.
	delete(got.Providers, "kimi")
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	if m := readAuth(t); len(m) != 0 {
		t.Fatalf("stale entry kept: %v", m)
	}
}

// Providers may share an entry; one whose key changes apart from the others
// moves to an entry of its own, and the rest keep theirs.
func TestSharedAuthKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := Default()
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://x", APIKey: "k1", AuthKey: "vendor"}
	cfg.Providers["b"] = Provider{Kind: KindCodex, BaseURL: "https://x", APIKey: "k1", AuthKey: "vendor"}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if m := readAuth(t); len(m) != 1 || m["vendor"] != "k1" {
		t.Fatalf("auth.json = %v", m)
	}
	p := cfg.Providers["b"]
	p.SetKey("k2")
	cfg.Providers["b"] = p
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if m := readAuth(t); m["vendor"] != "k1" || m["b"] != "k2" || cfg.Providers["b"].AuthKey != "b" {
		t.Fatalf("auth.json = %v, b's auth_key %q", m, cfg.Providers["b"].AuthKey)
	}
	// Moving never loses a key, even when the entry it lands on is shared.
	cfg.Providers["b"] = Provider{Kind: KindCodex, BaseURL: "https://x", APIKey: "k3", AuthKey: "vendor"}
	cfg.Providers["c"] = Provider{Kind: KindCodex, BaseURL: "https://x", APIKey: "k4", AuthKey: "b"}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a": "k1", "b": "k3", "c": "k4"} {
		if got := readAuth(t)[cfg.Providers[name].AuthKey]; got != want {
			t.Errorf("%s: key %q, want %q", name, got, want)
		}
	}
}
