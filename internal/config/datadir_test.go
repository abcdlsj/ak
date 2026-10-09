package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// State an older ak left in ~/.local/share/ak moves into ~/.config/ak, without
// overwriting a file already there.
func TestDataDirMigratesLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	migrateOnce = sync.Once{}
	t.Cleanup(func() { migrateOnce = sync.Once{} })

	old := filepath.Join(home, ".local", "share", "ak")
	cfgDir := filepath.Join(home, ".config", "ak")
	for _, d := range []string{old, cfgDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(old, "sessions.jsonl"), "old-sessions")
	write(filepath.Join(old, "recent.json"), "old-recent")
	write(filepath.Join(cfgDir, "recent.json"), "new-recent")

	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != cfgDir {
		t.Fatalf("DataDir = %s, want %s", dir, cfgDir)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	if got := read(filepath.Join(cfgDir, "sessions.jsonl")); got != "old-sessions" {
		t.Errorf("sessions.jsonl = %q", got)
	}
	if got := read(filepath.Join(cfgDir, "recent.json")); got != "new-recent" {
		t.Errorf("an existing file was overwritten: %q", got)
	}
	if got := read(filepath.Join(old, "recent.json")); got != "old-recent" {
		t.Errorf("the conflicting file was not left in place: %q", got)
	}
}
