package recent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRememberLookup(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo.git")
	a, b, other := filepath.Join(root, "wt-a"), filepath.Join(root, "wt-b"), filepath.Join(root, "other")
	for _, d := range []string{repo, a, b, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: filepath.Join(root, "data", "recent.json"), Repo: func(dir string) string {
		if dir == canonical(other) {
			return ""
		}
		return canonical(repo)
	}}
	all := func(string) bool { return true }

	if err := s.Remember(link, "kimi"); err != nil {
		t.Fatal(err)
	}
	if got := s.Lookup(a, all); got != "kimi" {
		t.Errorf("same dir through a symlink: %q", got)
	}
	// A worktree with no entry of its own falls back to its repository's.
	if got := s.Lookup(b, all); got != "kimi" {
		t.Errorf("new worktree: %q", got)
	}
	if got := s.Lookup(other, all); got != "" {
		t.Errorf("unrelated dir: %q", got)
	}
	// A provider since removed is skipped.
	if got := s.Lookup(a, func(n string) bool { return n != "kimi" }); got != "" {
		t.Errorf("removed provider: %q", got)
	}
	// Directories that are gone are forgotten on the next write.
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember(b, "cpa"); err != nil {
		t.Fatal(err)
	}
	if f := s.load(); len(f.Dirs) != 1 {
		t.Errorf("dirs = %v", f.Dirs)
	}
}
