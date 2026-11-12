package quota

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/abcdlsj/ak/internal/config"
)

//go:embed plugins/*.toml
var embedded embed.FS

// goSources are the vendors a single request and a few paths cannot describe.
var goSources = []Source{openrouter{}, moonshot{}, opendesign{}, remote{}}

// builtins are the Go sources then the embedded plugins, by file name.
var builtins = func() []Source {
	out := append([]Source(nil), goSources...)
	entries, err := embedded.ReadDir("plugins")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		name := path.Join("plugins", e.Name())
		b, err := embedded.ReadFile(name)
		if err != nil {
			panic(err)
		}
		pl, err := parsePlugin(name, b)
		if err != nil {
			panic(err) // a broken built-in is a build mistake; tests catch it
		}
		out = append(out, pl)
	}
	return out
}()

// registry is the sources in effect: the user's plugins first, so they win
// auto-detection, then the built-ins they do not override.
type registry struct {
	sources []Source
	errs    []error // user plugin files that did not load
}

// loadRegistry reads the user's plugins from dir: TOML specs and
// executables. A file that fails is reported and skipped; the rest still
// load. An empty or missing dir is fine.
func loadRegistry(dir string) registry {
	var r registry
	seen := map[string]bool{}
	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			r.errs = append(r.errs, fmt.Errorf("quota plugins: %w", err))
		}
		add := func(src Source, file string) {
			if seen[src.ID()] {
				r.errs = append(r.errs, fmt.Errorf("%s: id %q is already declared by another file", file, src.ID()))
				return
			}
			seen[src.ID()] = true
			r.sources = append(r.sources, src)
		}
		for _, e := range entries {
			file := filepath.Join(dir, e.Name())
			if !strings.HasSuffix(e.Name(), ".toml") {
				if pr, ok := asProgram(dir, e); ok {
					add(pr, file)
				}
				continue
			}
			b, err := os.ReadFile(file)
			if err == nil {
				var pl *plugin
				if pl, err = parsePlugin(file, b); err == nil {
					add(pl, file)
				}
			}
			if err != nil {
				r.errs = append(r.errs, err)
			}
		}
	}
	for _, s := range builtins {
		if !seen[s.ID()] {
			r.sources = append(r.sources, s)
		}
	}
	return r
}

var (
	regMu sync.Mutex
	reg   *registry
)

// current loads the registry from <config dir>/quota.d once per process.
func current() registry {
	regMu.Lock()
	defer regMu.Unlock()
	if reg == nil {
		dir, err := config.Dir()
		if err != nil {
			dir = ""
		} else {
			dir = filepath.Join(dir, "quota.d")
		}
		r := loadRegistry(dir)
		reg = &r
	}
	return *reg
}

// PluginErrors lists the user plugin files that failed to load.
func PluginErrors() []error { return current().errs }
