package bind

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/ak/internal/recent"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo", ".git")
	for _, d := range []string{repo, filepath.Join(root, "repo", "sub"), filepath.Join(root, "wt"), filepath.Join(root, "plain", "deep")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inRepo := map[string]bool{
		recent.Canonical(filepath.Join(root, "repo")):        true,
		recent.Canonical(filepath.Join(root, "repo", "sub")): true,
		recent.Canonical(filepath.Join(root, "wt")):          true,
	}
	s := &Store{Path: filepath.Join(root, "data", "bindings.json"), Repo: func(dir string) string {
		if inRepo[dir] {
			return recent.Canonical(repo)
		}
		return ""
	}}
	return s, root
}

func TestBindRepoCoversWorktrees(t *testing.T) {
	s, root := newStore(t)
	if _, err := s.Set(filepath.Join(root, "repo", "sub"), "claude", "kimi:high"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"repo", "wt"} {
		b, ok := s.Lookup(filepath.Join(root, d), "claude")
		if !ok || b.Target != "kimi:high" {
			t.Errorf("%s: %+v %v", d, b, ok)
		}
	}
	if _, ok := s.Lookup(filepath.Join(root, "repo"), "codex"); ok {
		t.Error("another engine is not bound")
	}
	if _, ok := s.Lookup(filepath.Join(root, "plain"), "claude"); ok {
		t.Error("outside the repository nothing is bound")
	}
}

func TestBindDirectoryCoversBelow(t *testing.T) {
	s, root := newStore(t)
	if _, err := s.Set(filepath.Join(root, "plain"), "codex", Native); err != nil {
		t.Fatal(err)
	}
	b, ok := s.Lookup(filepath.Join(root, "plain", "deep"), "codex")
	if !ok || b.Target != Native {
		t.Fatalf("below a bound directory: %+v %v", b, ok)
	}
	if _, removed, err := s.Unset(filepath.Join(root, "plain", "deep"), ""); err != nil || removed {
		t.Fatalf("unset below the place removed something: %v %v", removed, err)
	}
	if _, removed, err := s.Unset(filepath.Join(root, "plain"), ""); err != nil || !removed {
		t.Fatalf("unset at the place: %v %v", removed, err)
	}
	if _, ok := s.Lookup(filepath.Join(root, "plain", "deep"), "codex"); ok {
		t.Fatal("still bound after unset")
	}
}

func TestRenameAndDrop(t *testing.T) {
	s, root := newStore(t)
	_, _ = s.Set(filepath.Join(root, "repo"), "claude", "kimi:opus")
	_, _ = s.Set(filepath.Join(root, "repo"), "codex", "kimicodex")
	_, _ = s.Set(filepath.Join(root, "plain"), "claude", "kimi")
	if err := s.Rename("kimi", "k2"); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range s.List() {
		got[b.Engine+"@"+filepath.Base(filepath.Dir(b.Place))+"/"+filepath.Base(b.Place)] = b.Target
	}
	if got["claude@repo/.git"] != "k2:opus" || got["codex@repo/.git"] != "kimicodex" {
		t.Fatalf("rename: %v", got)
	}
	if err := s.Drop("k2"); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Target != "kimicodex" {
		t.Fatalf("drop: %+v", list)
	}
}
