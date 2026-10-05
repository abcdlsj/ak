package shim

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/secrets"
)

// shimMode is the permission mode of a shim. 700 rather than 755: the file
// embeds a plaintext key, so other users on the machine get no read access.
const shimMode = 0o700

// Action is the outcome for a single file.
type Action string

const (
	ActionCreated   Action = "created"
	ActionUpdated   Action = "updated"
	ActionUnchanged Action = "unchanged"
	ActionRemoved   Action = "removed"
	ActionSkipped   Action = "skipped"
)

// Result is one sync result.
type Result struct {
	Path   string
	Action Action
	Reason string // Explains why the file was skipped
}

// Report holds all results of one sync.
type Report struct {
	Results []Result
	DryRun  bool
}

// Counts summarizes results by action.
func (r Report) Counts() map[Action]int {
	m := map[Action]int{}
	for _, res := range r.Results {
		m[res.Action]++
	}
	return m
}

// Syncer generates and reclaims ak's artifacts.
type Syncer struct {
	Cfg      *config.Config
	Resolver secrets.Resolver
	DryRun   bool

	// CodexHome is the directory scanned for codex profile files. It is a field
	// rather than a hardcoded ~/.codex so tests stay hermetic instead of reaching
	// into the real home directory.
	CodexHome string
	// PiHome is pi's agent directory, where models.json is written. Also a field
	// so tests stay hermetic.
	PiHome string
	// Self is ak's own path, embedded in commands that resolve their key at
	// run time. Empty means the running executable.
	Self string
}

// Sync generates every provider's artifacts, then reclaims orphaned ones.
func (s *Syncer) Sync() (Report, error) {
	rep := Report{DryRun: s.DryRun}

	binDir := config.ExpandHome(s.Cfg.Settings.BinDir)
	if !s.DryRun {
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return rep, err
		}
	}

	ctx := provider.Context{CodexHome: s.codexHome(), PiHome: s.piHome(), Gateway: s.Cfg.Settings.GatewayURL()}
	// Record the artifacts that should exist this run; any other marked file
	// in the same directories is treated as an orphan.
	want := map[string]bool{}
	write := func(path, content string, mode os.FileMode) {
		want[path] = true
		rep.Results = append(rep.Results, s.writeFile(path, content, mode))
	}

	for _, name := range s.Cfg.Names() {
		p := s.Cfg.Providers[name]
		eng := provider.EngineFor(p.Kind)
		if eng == nil {
			return rep, fmt.Errorf("provider %s: unsupported kind %q", name, p.Kind)
		}
		key, err := s.secret(p)
		if err != nil {
			return rep, fmt.Errorf("secret for provider %s: %w", name, err)
		}
		launch, err := eng.Launch(name, p, key, ctx)
		if err != nil {
			return rep, err
		}

		kind := string(p.Kind)
		path := filepath.Join(binDir, s.Cfg.Settings.Prefix+name)
		write(path, Render(Spec{
			Name: name, Kind: kind,
			Bin:     s.resolveBin(eng.ConfiguredBin(s.Cfg.Settings), eng.BinName()),
			BinName: eng.BinName(), EnvVar: eng.BinEnvVar(),
			Launch: launch, Self: s.self(),
		}), shimMode)

		for _, f := range launch.Files {
			write(f.Path, withMarker(kind, name, f.Body), 0o600)
		}
		for _, v := range launch.Variants {
			if v.Shim {
				alias := filepath.Join(binDir, s.Cfg.Settings.Prefix+name+"-"+v.Name)
				write(alias, RenderAlias(kind, name, path, v.Name), shimMode)
			}
		}
	}

	// Engines that keep one shared file (pi's models.json) write it once here.
	for _, eng := range provider.Engines() {
		sw, ok := eng.(provider.SharedWriter)
		if !ok {
			continue
		}
		path, content, err := sw.Shared(ctx, s.Cfg)
		if err != nil {
			return rep, err
		}
		if path == "" {
			continue
		}
		want[path] = true
		rep.Results = append(rep.Results, s.writeShared(path, content))
	}

	orphans, err := s.collectOrphans(binDir, ctx, want)
	if err != nil {
		return rep, err
	}
	rep.Results = append(rep.Results, orphans...)

	sort.Slice(rep.Results, func(i, j int) bool { return rep.Results[i].Path < rep.Results[j].Path })
	return rep, nil
}

// writeShared writes an engine's shared file (pi's models.json). There is no
// marker check: the engine only replaces its own namespaced keys, and the
// content it returns is the whole file. An unchanged file is not rewritten.
func (s *Syncer) writeShared(path string, content []byte) Result {
	existing, err := os.ReadFile(path)
	// Keep the mode the file already has, so ak does not change it out from
	// under the engine that owns it.
	mode := os.FileMode(0o644)
	if err == nil {
		if fi, statErr := os.Stat(path); statErr == nil {
			mode = fi.Mode().Perm()
		}
	}
	switch {
	case err == nil && bytes.Equal(existing, content):
		return Result{Path: path, Action: ActionUnchanged}
	case err == nil:
		if s.DryRun {
			return Result{Path: path, Action: ActionUpdated}
		}
		if err := config.AtomicWrite(path, content, mode); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		return Result{Path: path, Action: ActionUpdated}
	case os.IsNotExist(err):
		if s.DryRun {
			return Result{Path: path, Action: ActionCreated}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		if err := config.AtomicWrite(path, content, mode); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		return Result{Path: path, Action: ActionCreated}
	default:
		return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
	}
}

// secret resolves a plaintext key now, or defers an api_key_ref to run time
// so the command never holds a plaintext copy.
func (s *Syncer) secret(p config.Provider) (provider.Secret, error) {
	if p.APIKeyRef != "" {
		return provider.Secret{Deferred: true}, nil
	}
	key, err := s.Resolver.Resolve(p)
	return provider.Literal(key), err
}

func (s *Syncer) self() string {
	if s.Self != "" {
		return s.Self
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return ""
}

// writeFile writes idempotently: unchanged content is not written to disk and mtime is untouched.
func (s *Syncer) writeFile(path, content string, mode os.FileMode) Result {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && string(existing) == content:
		return Result{Path: path, Action: ActionUnchanged}
	case err == nil:
		// Exists but differs: only overwrite after confirming ak generated it,
		// otherwise a user's own script could be clobbered.
		if _, ok := readMarker(path); !ok {
			return Result{Path: path, Action: ActionSkipped,
				Reason: "exists and was not generated by ak"}
		}
		if s.DryRun {
			return Result{Path: path, Action: ActionUpdated}
		}
		if err := config.AtomicWrite(path, []byte(content), mode); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		return Result{Path: path, Action: ActionUpdated}
	case os.IsNotExist(err):
		if s.DryRun {
			return Result{Path: path, Action: ActionCreated}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		if err := config.AtomicWrite(path, []byte(content), mode); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		return Result{Path: path, Action: ActionCreated}
	default:
		return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
	}
}

func (s *Syncer) collectOrphans(binDir string, ctx provider.Context, want map[string]bool) ([]Result, error) {
	type dirPrefix struct{ dir, prefix string }
	dirs := []dirPrefix{{binDir, s.Cfg.Settings.Prefix}}
	for _, eng := range provider.Engines() {
		for _, d := range eng.ArtifactDirs(ctx) {
			dirs = append(dirs, dirPrefix{d, "ak-"})
		}
	}

	var out []Result
	for _, d := range dirs {
		res, err := s.collectOrphansIn(d.dir, d.prefix, want)
		out = append(out, res...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// collectOrphansIn reclaims orphans within a single directory.
//
// All five conditions must hold before deletion; if any fails the file is skipped:
//  1. It lives in the given directory
//  2. Its name matches the prefix
//  3. It is a regular file, with no symlink following
//  4. Its owner is the current user
//  5. Its header carries a known ak:generated marker version
//
// ~/.local/bin holds user-owned tools such as warren, minions, agy and aicoding,
// so deleting the wrong file is extremely costly.
func (s *Syncer) collectOrphansIn(dir, prefix string, want map[string]bool) ([]Result, error) {
	var out []Result

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue // Prefix mismatch: the first line of defense
		}
		path := filepath.Join(dir, name)
		if want[path] {
			continue
		}
		if res, ok := s.checkRemovable(path, e); !ok {
			out = append(out, res)
			continue
		}
		if s.DryRun {
			out = append(out, Result{Path: path, Action: ActionRemoved})
			continue
		}
		if err := os.Remove(path); err != nil {
			out = append(out, Result{Path: path, Action: ActionSkipped, Reason: err.Error()})
			continue
		}
		out = append(out, Result{Path: path, Action: ActionRemoved})
	}
	return out, nil
}

// checkRemovable runs every safety check before deletion.
func (s *Syncer) checkRemovable(path string, e os.DirEntry) (Result, bool) {
	// Use Lstat rather than Stat: do not follow symlinks, which could delete a
	// critical file through the link.
	fi, err := os.Lstat(path)
	if err != nil {
		return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}, false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Result{Path: path, Action: ActionSkipped, Reason: "is a symlink, refusing to follow"}, false
	}
	if !fi.Mode().IsRegular() {
		return Result{Path: path, Action: ActionSkipped, Reason: "not a regular file"}, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			return Result{Path: path, Action: ActionSkipped, Reason: "owner is not the current user"}, false
		}
	}
	m, ok := readMarker(path)
	if !ok {
		return Result{Path: path, Action: ActionSkipped, Reason: "no ak:generated marker"}, false
	}
	if m.Version > MarkerVersion {
		return Result{Path: path, Action: ActionSkipped,
			Reason: fmt.Sprintf("marker version v%d is newer than the supported v%d", m.Version, MarkerVersion)}, false
	}
	return Result{}, true
}

// piHome returns pi's agent directory, honouring PI_CODING_AGENT_DIR the way pi
// itself does, so ak writes models.json where the shim's pi will read it.
func (s *Syncer) piHome() string {
	if s.PiHome != "" {
		return s.PiHome
	}
	if v := os.Getenv(provider.EnvPiHome); v != "" {
		return config.ExpandHome(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// codexHome returns the directory holding codex profiles.
// It is a field on Syncer rather than a hardcoded ~/.codex so tests stay hermetic
// instead of reaching into the real home directory.
func (s *Syncer) codexHome() string {
	if s.CodexHome != "" {
		return s.CodexHome
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// resolveBin resolves the engine binary path.
// It takes only the layer found via PATH and does not call EvalSymlinks —
// claude's ~/.local/bin/claude is a symlink to versions/<ver>, and resolving it
// would leave every shim pointing at an old version after a self-update.
func (s *Syncer) resolveBin(configured, name string) string {
	if configured != "" {
		return config.ExpandHome(configured)
	}
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return ""
}
