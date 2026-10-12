// Package bind records which provider an engine command (claude, codex, pi)
// runs on in a repository or directory, so `claude` typed there goes through
// ak while everywhere else it stays the engine's own. The record lives in ak's
// data dir; nothing is written into the directories themselves.
package bind

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/recent"
)

// Native is the target that keeps an engine's own command, skipping ak.
const Native = "native"

// file maps a place (a repository's common git dir, or a directory) to the
// target each engine runs on there.
type file struct {
	Version  int                          `json:"version"`
	Bindings map[string]map[string]string `json:"bindings"`
}

// Store reads and writes the record at Path.
type Store struct {
	Path string
	// Repo returns the repository dir is in (git's common dir), or "" when it
	// is in none. A field so tests need no git.
	Repo func(dir string) string
}

// Binding is one engine's target at one place.
type Binding struct {
	// Place is the repository's common git dir or the directory bound.
	Place  string
	Engine string
	Target string
}

// Default is the store in ak's data dir.
func Default() (*Store, error) {
	dir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "bindings.json"), Repo: recent.GitCommonDir}, nil
}

// Place is where a binding made in dir is recorded: its repository, so every
// worktree shares it, else the directory itself.
func (s *Store) Place(dir string) string {
	dir = recent.Canonical(dir)
	if repo := s.Repo(dir); repo != "" {
		return repo
	}
	return dir
}

// Set binds engine to target at dir's place and returns the place.
func (s *Store) Set(dir, engine, target string) (string, error) {
	place := s.Place(dir)
	f := s.load()
	if f.Bindings[place] == nil {
		f.Bindings[place] = map[string]string{}
	}
	f.Bindings[place][engine] = target
	return place, s.save(f)
}

// Unset removes the binding of engine, or of every engine when engine is "",
// at dir's place, and reports whether anything was removed.
func (s *Store) Unset(dir, engine string) (string, bool, error) {
	place := s.Place(dir)
	f := s.load()
	m := f.Bindings[place]
	if len(m) == 0 {
		return place, false, nil
	}
	removed := false
	if engine == "" {
		removed = true
		delete(f.Bindings, place)
	} else if _, ok := m[engine]; ok {
		removed = true
		delete(m, engine)
		if len(m) == 0 {
			delete(f.Bindings, place)
		}
	}
	if !removed {
		return place, false, nil
	}
	return place, true, s.save(f)
}

// Lookup returns engine's binding in effect at dir: its repository's, else
// the nearest bound directory at or above dir.
func (s *Store) Lookup(dir, engine string) (Binding, bool) {
	dir = recent.Canonical(dir)
	f := s.load()
	if repo := s.Repo(dir); repo != "" {
		if t, ok := f.Bindings[repo][engine]; ok {
			return Binding{Place: repo, Engine: engine, Target: t}, true
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		if t, ok := f.Bindings[d][engine]; ok {
			return Binding{Place: d, Engine: engine, Target: t}, true
		}
		if filepath.Dir(d) == d {
			return Binding{}, false
		}
	}
}

// List returns every binding, sorted by place then engine.
func (s *Store) List() []Binding {
	f := s.load()
	var out []Binding
	for place, m := range f.Bindings {
		for engine, t := range m {
			out = append(out, Binding{Place: place, Engine: engine, Target: t})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Place != out[j].Place {
			return out[i].Place < out[j].Place
		}
		return out[i].Engine < out[j].Engine
	})
	return out
}

// Rename points every binding of provider from at to, keeping its variant.
func (s *Store) Rename(from, to string) error {
	return s.rewrite(func(t string) (string, bool) {
		if name, variant, ok := strings.Cut(t, ":"); name == from {
			if ok {
				return to + ":" + variant, true
			}
			return to, true
		}
		return t, true
	})
}

// Drop removes every binding of provider name.
func (s *Store) Drop(name string) error {
	return s.rewrite(func(t string) (string, bool) {
		n, _, _ := strings.Cut(t, ":")
		return t, n != name
	})
}

// rewrite applies change to every target, dropping those it rejects, and
// saves only when something changed.
func (s *Store) rewrite(change func(string) (string, bool)) error {
	if _, err := os.Stat(s.Path); os.IsNotExist(err) {
		return nil
	}
	f := s.load()
	changed := false
	for place, m := range f.Bindings {
		for engine, t := range m {
			nt, keep := change(t)
			switch {
			case !keep:
				delete(m, engine)
				changed = true
			case nt != t:
				m[engine] = nt
				changed = true
			}
		}
		if len(m) == 0 {
			delete(f.Bindings, place)
		}
	}
	if !changed {
		return nil
	}
	return s.save(f)
}

func (s *Store) load() file {
	f := file{}
	if data, err := os.ReadFile(s.Path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Bindings == nil {
		f.Bindings = map[string]map[string]string{}
	}
	return f
}

func (s *Store) save(f file) error {
	// Forget places that are gone, so the record does not grow forever.
	for place := range f.Bindings {
		if _, err := os.Stat(place); os.IsNotExist(err) {
			delete(f.Bindings, place)
		}
	}
	f.Version = 1
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return config.AtomicWrite(s.Path, data, 0o600)
}
