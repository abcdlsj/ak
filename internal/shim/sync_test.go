package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// TestCheckRemovable_Safety is the highest-priority test: wrongly deleting a
// user's own tool is extremely costly. ~/.local/bin holds real tools such as
// warren, minions, agy and aicoding.
func TestCheckRemovable_Safety(t *testing.T) {
	dir := t.TempDir()
	s := &Syncer{Cfg: config.Default()}

	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}

	generated := "#!/usr/bin/env bash\n# ak:generated v1 kind=claude provider=x hash=abc\necho hi\n"

	tests := []struct {
		name    string
		path    string
		wantOK  bool
		reasonC string // Substring the reason is expected to contain
	}{
		{
			name:   "marked artifact can be deleted",
			path:   write("ak-generated", generated, 0o700),
			wantOK: true,
		},
		{
			name:    "user's own same-prefix script cannot be deleted",
			path:    write("ak-mine", "#!/bin/bash\necho my own script\n", 0o755),
			wantOK:  false,
			reasonC: "no ak:generated marker",
		},
		{
			name:    "marker version newer than this binary is skipped",
			path:    write("ak-future", "#!/usr/bin/env bash\n# ak:generated v99 kind=claude provider=y hash=z\n", 0o700),
			wantOK:  false,
			reasonC: "newer than the supported",
		},
		{
			name:    "empty file cannot be deleted",
			path:    write("ak-empty", "", 0o700),
			wantOK:  false,
			reasonC: "marker",
		},
		{
			name:    "marker appearing too late is not recognized",
			path:    write("ak-late", strings.Repeat("# filler\n", 10)+"# ak:generated v1 kind=claude provider=z hash=q\n", 0o700),
			wantOK:  false,
			reasonC: "marker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := dirEntryOf(t, tt.path)
			res, ok := s.checkRemovable(tt.path, e)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (reason=%q)", ok, tt.wantOK, res.Reason)
			}
			if !ok && tt.reasonC != "" && !strings.Contains(res.Reason, tt.reasonC) {
				t.Errorf("reason = %q, want it to contain %q", res.Reason, tt.reasonC)
			}
		})
	}
}

// TestCheckRemovable_RefusesSymlink confirms symlinks are not followed,
// otherwise a critical file could be deleted through the link.
func TestCheckRemovable_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	s := &Syncer{Cfg: config.Default()}

	// The target file carries a valid marker itself, but access via symlink
	// must still be refused.
	target := filepath.Join(dir, "real-target")
	if err := os.WriteFile(target, []byte("# ak:generated v1 kind=claude provider=x hash=a\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ak-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	res, ok := s.checkRemovable(link, dirEntryOf(t, link))
	if ok {
		t.Fatal("symlink was judged deletable, which would wrongly delete the link target")
	}
	if !strings.Contains(res.Reason, "symlink") {
		t.Errorf("reason = %q, want it to mention symlink", res.Reason)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Errorf("the link target should be unaffected: %v", err)
	}
}

// TestCollectOrphans_SkipsForeignPrefix confirms files with a mismatched prefix
// never enter the candidate set.
func TestCollectOrphans_SkipsForeignPrefix(t *testing.T) {
	dir := t.TempDir()
	// Simulate real user tools in ~/.local/bin.
	for _, n := range []string{"warren", "minions", "agy", "aicoding", "codex-cpa"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	// Point CodexHome at an empty dir so collectOrphans cannot sweep the real
	// ~/.codex and leak ak's genuine artifacts into this test.
	s := &Syncer{Cfg: cfg, DryRun: true, CodexHome: t.TempDir()}

	results, err := s.collectOrphansIn(dir, cfg.Settings.Prefix, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		t.Errorf("user tool %s should not appear in the results (action=%s)", r.Path, r.Action)
	}

	// All files must still be present.
	for _, n := range []string{"warren", "minions", "agy", "aicoding", "codex-cpa"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was deleted: %v", n, err)
		}
	}
}

// TestWriteFile_RefusesUnmarkedOverwrite confirms a user's own file with the same
// name is not overwritten.
func TestWriteFile_RefusesUnmarkedOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ak-mine")
	original := "#!/bin/bash\necho user script\n"
	if err := os.WriteFile(p, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}

	s := &Syncer{Cfg: config.Default()}
	res := s.writeFile(p, "#!/usr/bin/env bash\n# ak:generated v1\n", 0o700)

	if res.Action != ActionSkipped {
		t.Fatalf("action = %s, want skipped", res.Action)
	}
	got, _ := os.ReadFile(p)
	if string(got) != original {
		t.Error("the user's file was overwritten")
	}
}

func dirEntryOf(t *testing.T, path string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == filepath.Base(path) {
			return e
		}
	}
	t.Fatalf("cannot find %s", path)
	return nil
}

// TestCollectOrphans_CodexHomeInjected confirms the codex directory can be injected.
// collectOrphans scans the real ~/.codex by default, so a test must be able to
// point it at a temp directory; otherwise results vary with whichever profiles
// happen to exist on the dev machine.
func TestCollectOrphans_CodexHomeInjected(t *testing.T) {
	binDir := t.TempDir()
	codexHome := t.TempDir()

	// A stale ak profile that should be reclaimed.
	stale := filepath.Join(codexHome, "ak-gone.config.toml")
	if err := os.WriteFile(stale,
		[]byte("# ak:generated v1 kind=codex provider=gone hash=x\nmodel = 'm'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	s := &Syncer{Cfg: cfg, DryRun: true, CodexHome: codexHome}

	results, err := s.collectOrphans(binDir, provider.Context{CodexHome: codexHome}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}

	var removed []string
	for _, r := range results {
		if r.Action == ActionRemoved {
			removed = append(removed, filepath.Base(r.Path))
		}
	}
	if len(removed) != 1 || removed[0] != "ak-gone.config.toml" {
		t.Fatalf("removed = %v, expected only ak-gone.config.toml", removed)
	}
}

// TestCodexHome_DefaultsToRealDir confirms it falls back to ~/.codex when not injected.
func TestCodexHome_DefaultsToRealDir(t *testing.T) {
	s := &Syncer{Cfg: config.Default()}
	got := s.codexHome()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".codex")
	if got != want {
		t.Errorf("codexHome() = %q, expected %q", got, want)
	}
}

func TestWriteSharedKeepsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Cfg: config.Default()}
	if res := s.writeShared(path, []byte(`{"providers":{}}`)); res.Action != ActionUpdated {
		t.Fatalf("action = %v (%s)", res.Action, res.Reason)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600 (ak changed the engine's file mode)", fi.Mode().Perm())
	}
}
