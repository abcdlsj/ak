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
			if !strings.Contains(out, "command ak __engine "+string(k)) {
				t.Errorf("%s: no function for %s:\n%s", sh, k, out)
			}
			cmd := k.CommandName()
			if sh == "fish" {
				if !strings.Contains(out, "function "+cmd+" --wraps "+cmd) {
					t.Errorf("%s: wrong fish function name for %s:\n%s", sh, k, out)
				}
			} else if !strings.Contains(out, "function "+cmd+" {") {
				t.Errorf("%s: wrong shell function name for %s:\n%s", sh, k, out)
			}
		}
	}
	if _, err := shellInit("tcsh", config.Kinds); err == nil {
		t.Error("an unknown shell must be refused")
	}
	// The functions must define even over an alias of the same name.
	for _, sh := range []string{"bash", "zsh"} {
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		out, _ := shellInit(sh, config.Kinds)
		script := "alias claude='echo aliased'\n"
		if sh == "bash" {
			script = "shopt -s expand_aliases\n" + script
		}
		script += out + "typeset -f claude | grep -q __engine && typeset -f pi | grep -q __engine && typeset -f agy | grep -q __engine"
		if b, err := exec.Command(bin, "-c", script).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", sh, err, b)
		}
	}
}

// One row per engine: what it runs on and why; a target that cannot run is
// marked broken in its row.
func TestWhichRows(t *testing.T) {
	t.Setenv(overrideEnv, "")
	root := t.TempDir()
	store := &bind.Store{Path: filepath.Join(root, "bindings.json"), Repo: func(string) string { return "" }}
	cfg := config.Default()
	cfg.Providers["kimi"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://k", APIKey: "k"}
	if _, err := store.Set(root, "claude", "kimi"); err != nil {
		t.Fatal(err)
	}
	rows, broken := whichRows(cfg, store, config.Kinds, root)
	if broken {
		t.Fatal("a valid binding is not broken")
	}
	if len(rows) != len(config.Kinds) {
		t.Fatalf("rows = %q", rows)
	}
	if rows[0][1] != "kimi" || !strings.Contains(rows[0][2], "bound at") {
		t.Errorf("claude row = %q", rows[0])
	}
	if rows[1][1] != bind.Native {
		t.Errorf("codex runs on %q, want native", rows[1][1])
	}
	if _, err := store.Set(root, "codex", "gone"); err != nil {
		t.Fatal(err)
	}
	rows, broken = whichRows(cfg, store, config.Kinds, root)
	if !broken {
		t.Fatal("a binding to a removed provider must be broken")
	}
	if !strings.HasPrefix(rows[1][0], "✗") || rows[1][1] != "gone" {
		t.Errorf("broken row = %q", rows[1])
	}
}

// The engine defaults come first, then the bindings, each with where it was
// set; nothing bound is empty.
func TestBindingRows(t *testing.T) {
	cfg := config.Default()
	cfg.Settings.DefaultCodex = "gpt"
	list := []bind.Binding{
		{Place: "/w/repo/.git", Engine: "claude", Target: "kimi"},
		{Place: "/w/repo/.git", Engine: "antigravity", Target: "work"},
	}
	rows := bindingRows(cfg, list)
	if len(rows) != 3 || rows[0][0] != "codex" || rows[0][2] != "default" || rows[1][2] != "/w/repo" || rows[2][0] != "agy" {
		t.Errorf("rows = %q", rows)
	}
	if !hasEngineDefaults(cfg) || hasEngineDefaults(config.Default()) {
		t.Error("hasEngineDefaults is wrong")
	}
}

func TestAgyBindAndWhichAlias(t *testing.T) {
	t.Setenv(overrideEnv, "")
	k, ok := parseKind("agy")
	if !ok || k != config.KindAntigravity {
		t.Fatalf("parseKind(agy) = %q, %v", k, ok)
	}
	root := t.TempDir()
	store := &bind.Store{Path: filepath.Join(root, "bindings.json"), Repo: func(string) string { return "" }}
	cfg := config.Default()
	cfg.Providers["work"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: filepath.Join(root, "agy-work")}
	if _, err := store.Set(root, string(k), "work"); err != nil {
		t.Fatal(err)
	}
	rows, broken := whichRows(cfg, store, []config.Kind{config.KindAntigravity}, root)
	if broken || len(rows) != 1 || rows[0][0] != "agy" || rows[0][1] != "work" {
		t.Fatalf("whichRows = %q, broken=%v", rows, broken)
	}
}
