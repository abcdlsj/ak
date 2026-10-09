package shim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// fakeAk writes an executable that prints its arguments, standing in for ak.
func fakeAk(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ak")
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\necho \"args=$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

type syncFixture struct {
	bin, codexHome, self string
	cfg                  *config.Config
}

func newSyncFixture(t *testing.T) *syncFixture {
	root := t.TempDir()
	f := &syncFixture{bin: filepath.Join(root, "bin"), codexHome: filepath.Join(root, "codex"),
		self: fakeAk(t), cfg: config.Default()}
	f.cfg.Settings.BinDir = f.bin
	return f
}

func (f *syncFixture) sync(t *testing.T) Report {
	t.Helper()
	if err := config.Validate(f.cfg); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Cfg: f.cfg, CodexHome: f.codexHome, Self: f.self}
	rep, err := s.Sync()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Action == ActionSkipped {
			t.Fatalf("skipped %s: %s", r.Path, r.Reason)
		}
	}
	return rep
}

func run(t *testing.T, path string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", path, args, err, out)
	}
	return string(out)
}

// A command forwards its arguments to `ak run`, and holds no key.
func TestCommandForwardsToAk(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKey: "sk-secret", Model: "m"}
	f.sync(t)
	path := filepath.Join(f.bin, "ak-cl")
	if out := run(t, path, nil, "-p", "it's"); out != "args=run cl -p it's\n" {
		t.Fatalf("out = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-secret") {
		t.Fatal("the key was written into the command")
	}
	// AK_BIN overrides the ak it calls.
	other := fakeAk(t)
	if out := run(t, path, []string{"AK_BIN=" + other}); out != "args=run cl\n" {
		t.Fatalf("AK_BIN: out = %q", out)
	}
}

// A standalone variant command launches provider:variant, and dropping the
// variant reclaims it.
func TestVariantCommand(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cx"] = config.Provider{
		Kind: config.KindCodex, BaseURL: "https://x", APIKey: "sk-1", Model: "base",
		Variants: map[string]config.Variant{"fast": {Model: "gpt-fast", Shim: true}},
	}
	f.sync(t)
	if out := run(t, filepath.Join(f.bin, "ak-cx-fast"), nil, "hi"); out != "args=run cx:fast hi\n" {
		t.Fatalf("out = %q", out)
	}
	p := f.cfg.Providers["cx"]
	p.Variants = nil
	f.cfg.Providers["cx"] = p
	f.sync(t)
	if _, err := os.Stat(filepath.Join(f.bin, "ak-cx-fast")); !os.IsNotExist(err) {
		t.Fatalf("stale alias not removed: %v", err)
	}
}

// sync tightens the mode of an unchanged command left looser by an older ak.
func TestSyncFixesLooseMode(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y", APIKey: "k", Model: "m"}
	f.sync(t)
	path := filepath.Join(f.bin, "ak-cl")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != shimMode {
		t.Errorf("mode = %v, want %v", fi.Mode().Perm(), shimMode)
	}
}
