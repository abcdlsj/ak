// Package recent remembers which provider was last launched in each
// directory, so the launcher opens on it. The record lives in ak's data dir;
// nothing is written into the directories themselves.
package recent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// file holds the last provider per directory and per repository. A new
// worktree has no directory entry yet; its repository's entry covers it.
type file struct {
	Dirs  map[string]entry `json:"dirs"`
	Repos map[string]entry `json:"repos"`
}

type entry struct {
	Provider string    `json:"provider"`
	At       time.Time `json:"at"`
}

// Store reads and writes the record at Path.
type Store struct {
	Path string
	// Repo returns the repository dir is in (git's common dir), or "" when it
	// is in none. A field so tests need no git.
	Repo func(dir string) string
}

// Default is the store in ak's data dir.
func Default() (*Store, error) {
	dir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "recent.json"), Repo: gitCommonDir}, nil
}

// Remember records provider as the last one launched in dir.
func (s *Store) Remember(dir, provider string) error {
	dir = canonical(dir)
	f := s.load()
	e := entry{Provider: provider, At: time.Now().UTC()}
	f.Dirs[dir] = e
	if repo := s.Repo(dir); repo != "" {
		f.Repos[repo] = e
	}
	// Forget directories that are gone, so the record does not grow forever.
	for _, m := range []map[string]entry{f.Dirs, f.Repos} {
		for k := range m {
			if _, err := os.Stat(k); os.IsNotExist(err) {
				delete(m, k)
			}
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return config.AtomicWrite(s.Path, data, 0o600)
}

// Lookup returns the provider last launched in dir, else in its repository,
// skipping any that valid rejects (a provider since removed).
func (s *Store) Lookup(dir string, valid func(string) bool) string {
	dir = canonical(dir)
	f := s.load()
	if e, ok := f.Dirs[dir]; ok && valid(e.Provider) {
		return e.Provider
	}
	if repo := s.Repo(dir); repo != "" {
		if e, ok := f.Repos[repo]; ok && valid(e.Provider) {
			return e.Provider
		}
	}
	return ""
}

func (s *Store) load() file {
	f := file{}
	if data, err := os.ReadFile(s.Path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Dirs == nil {
		f.Dirs = map[string]entry{}
	}
	if f.Repos == nil {
		f.Repos = map[string]entry{}
	}
	return f
}

// canonical resolves symlinks, so one directory reached two ways is one entry.
func canonical(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// gitCommonDir is the repository's shared .git dir, the same for every
// worktree of it.
func gitCommonDir(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return canonical(strings.TrimSpace(string(out)))
}
