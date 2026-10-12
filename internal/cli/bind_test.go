package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/bind"
	"github.com/abcdlsj/ak/internal/config"
)

func TestResolveEngineOrder(t *testing.T) {
	t.Setenv(overrideEnv, "")
	root := t.TempDir()
	repo, plain := filepath.Join(root, "repo"), filepath.Join(root, "plain")
	for _, d := range []string{repo, plain} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := &bind.Store{Path: filepath.Join(root, "bindings.json"), Repo: func(string) string { return "" }}
	cfg := config.Default()
	cfg.Providers["kimi"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://k", APIKey: "k"}
	cfg.Providers["ds"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://d", APIKey: "k"}
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://c", APIKey: "k"}

	check := func(dir string, k config.Kind, want string) {
		t.Helper()
		r, err := resolveEngine(cfg, store, k, dir)
		if err != nil {
			t.Fatalf("%s in %s: %v", k, dir, err)
		}
		if r.Target != want {
			t.Fatalf("%s in %s = %q (%s), want %q", k, dir, r.Target, r.Source, want)
		}
	}
	check(plain, config.KindClaude, "") // nothing set: the engine's own command
	cfg.Settings.DefaultClaude = "ds"
	check(plain, config.KindClaude, "ds")
	check(plain, config.KindCodex, "")
	if _, err := store.Set(repo, "claude", "kimi:opus"); err != nil {
		t.Fatal(err)
	}
	check(repo, config.KindClaude, "kimi:opus")
	if _, err := store.Set(plain, "claude", bind.Native); err != nil {
		t.Fatal(err)
	}
	check(plain, config.KindClaude, "") // native beats the default
	t.Setenv(overrideEnv, "ds")
	check(repo, config.KindClaude, "ds") // the override beats the binding
	t.Setenv(overrideEnv, "cx")
	if _, err := resolveEngine(cfg, store, config.KindClaude, repo); err == nil {
		t.Fatal("a codex provider for claude must fail, not run natively")
	}
	t.Setenv(overrideEnv, "")
	if _, err := store.Set(repo, "claude", "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveEngine(cfg, store, config.KindClaude, repo); err == nil {
		t.Fatal("a binding to a removed provider must fail")
	}
}

func TestShellInitRuns(t *testing.T) {
	for _, sh := range []string{"zsh", "bash", "fish"} {
		out, err := shellInit(sh, config.Kinds)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range config.Kinds {
			if !strings.Contains(out, "command ak __resolve "+string(k)) {
				t.Errorf("%s: no function for %s:\n%s", sh, k, out)
			}
		}
	}
	if _, err := shellInit("tcsh", config.Kinds); err == nil {
		t.Error("an unknown shell must be refused")
	}
	// The bash functions must at least parse.
	if bash, err := exec.LookPath("bash"); err == nil {
		out, _ := shellInit("bash", config.Kinds)
		if b, err := exec.Command(bash, "-n", "-c", out).CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, b)
		}
	}
}
