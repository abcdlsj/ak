package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShellIdempotentAndReversible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	// A dotfiles-managed rc: the link must stay a link.
	dotfiles := filepath.Join(home, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(dotfiles), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "export EDITOR=vim\nalias ll='ls -l'"
	if err := os.WriteFile(dotfiles, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	if err := os.Symlink(dotfiles, rc); err != nil {
		t.Fatal(err)
	}

	if _, changed, err := installShell("zsh"); err != nil || !changed {
		t.Fatalf("first install: %v %v", changed, err)
	}
	if _, changed, err := installShell("zsh"); err != nil || changed {
		t.Fatalf("second install changed the file: %v %v", changed, err)
	}
	if fi, err := os.Lstat(rc); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rc is no longer a symlink: %v", err)
	}
	data, _ := os.ReadFile(dotfiles)
	if strings.Count(string(data), rcBegin) != 1 || !strings.Contains(string(data), `eval "$(ak init zsh)"`) {
		t.Fatalf("rc after install:\n%s", data)
	}
	if fi, _ := os.Stat(dotfiles); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v", fi.Mode().Perm())
	}
	if _, removed, err := uninstallShell("zsh"); err != nil || !removed {
		t.Fatalf("uninstall: %v %v", removed, err)
	}
	data, _ = os.ReadFile(dotfiles)
	if strings.TrimRight(string(data), "\n") != orig {
		t.Fatalf("rc after uninstall:\n%q", data)
	}

	path, changed, err := installShell("fish")
	if err != nil || !changed || path != filepath.Join(home, ".config", "fish", "conf.d", "ak.fish") {
		t.Fatalf("fish install: %s %v %v", path, changed, err)
	}
	if _, removed, err := uninstallShell("fish"); err != nil || !removed {
		t.Fatalf("fish uninstall: %v %v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("fish file left behind")
	}
}
