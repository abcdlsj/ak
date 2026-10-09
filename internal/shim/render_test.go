package shim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// literalResolver returns the configured api_key as-is.
type literalResolver struct{}

func (literalResolver) Resolve(p config.Provider) (string, error) { return p.APIKey, nil }

// fakeBin writes an executable that prints its arguments and selected
// environment variables, standing in for claude or codex.
func fakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := "#!/usr/bin/env bash\n" +
		"echo \"args=$*\"\n" +
		"env | grep -E '^(AK_|ANTHROPIC_|HTTPS_PROXY|CODEX_HOME)' | sort\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

type syncFixture struct {
	bin, codexHome string
	cfg            *config.Config
}

func newSyncFixture(t *testing.T) *syncFixture {
	root := t.TempDir()
	f := &syncFixture{bin: filepath.Join(root, "bin"), codexHome: filepath.Join(root, "codex"), cfg: config.Default()}
	engines := filepath.Join(root, "engines")
	_ = os.MkdirAll(engines, 0o755)
	f.cfg.Settings.BinDir = f.bin
	f.cfg.Settings.ClaudeBin = fakeBin(t, engines, "claude")
	f.cfg.Settings.CodexBin = fakeBin(t, engines, "codex")
	return f
}

func (f *syncFixture) sync(t *testing.T, self string) Report {
	t.Helper()
	if err := config.Validate(f.cfg); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Cfg: f.cfg, Resolver: literalResolver{}, CodexHome: f.codexHome, Self: self}
	rep, err := s.Sync()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Action == ActionSkipped {
			t.Fatalf("skipped %s: %s", r.Path, r.Reason)
		}
	}
	return rep
}

func run(t *testing.T, path string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", path, args, err, out)
	}
	return string(out)
}

func TestCodexCustomVariantSetsModel(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cx"] = config.Provider{
		Kind: config.KindCodex, BaseURL: "https://x", APIKey: "sk-1", Model: "base",
		Variants: map[string]config.Variant{"fast": {Model: "gpt-fast", Reasoning: "low", Shim: true}},
	}
	f.sync(t, "")

	out := run(t, filepath.Join(f.bin, "ak-cx"), nil, "fast", "hello")
	if !strings.Contains(out, `-c model="gpt-fast" -c model_reasoning_effort="low" hello`) {
		t.Fatalf("custom variant args wrong:\n%s", out)
	}
	if !strings.Contains(out, `model_provider="cx"`) {
		t.Fatalf("base provider not selected:\n%s", out)
	}
	if strings.Contains(out, "--profile") {
		t.Fatalf("a profile file is still used:\n%s", out)
	}
	if strings.Contains(out, `model_reasoning_effort="fast"`) {
		t.Fatalf("custom variant name leaked as a reasoning level:\n%s", out)
	}
	out = run(t, filepath.Join(f.bin, "ak-cx"), nil, "high")
	if !strings.Contains(out, `-c model_reasoning_effort="high"`) {
		t.Fatalf("reasoning variant args wrong:\n%s", out)
	}
	// The standalone variant command behaves like `ak-cx fast`.
	out = run(t, filepath.Join(f.bin, "ak-cx-fast"), nil, "hi")
	if !strings.Contains(out, `-c model="gpt-fast"`) || !strings.Contains(out, "AK_VARIANT=fast") {
		t.Fatalf("alias output wrong:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.codexHome, "ak-cx.config.toml")); !os.IsNotExist(err) {
		t.Fatalf("a codex profile file was written: %v", err)
	}

	// Dropping the variant reclaims its standalone command.
	p := f.cfg.Providers["cx"]
	p.Variants = nil
	f.cfg.Providers["cx"] = p
	f.sync(t, "")
	if _, err := os.Stat(filepath.Join(f.bin, "ak-cx-fast")); !os.IsNotExist(err) {
		t.Fatalf("stale alias not removed: %v", err)
	}
}

// A parent's variant must not leak into a nested launch of another provider.
func TestVariantDoesNotLeakFromParent(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKey: "k",
		Model: "m", Opus: "big"}
	f.sync(t, "")
	out := run(t, filepath.Join(f.bin, "ak-cl"), []string{"AK_VARIANT=opus"})
	if !strings.Contains(out, "ANTHROPIC_MODEL=m\n") || strings.Contains(out, "AK_VARIANT") {
		t.Fatalf("inherited AK_VARIANT applied:\n%s", out)
	}
	out = run(t, filepath.Join(f.bin, "ak-cl"), nil, "opus")
	if !strings.Contains(out, "ANTHROPIC_MODEL=big\n") {
		t.Fatalf("opus variant not applied:\n%s", out)
	}
}

// With api_key_ref the command holds no plaintext key and asks ak at launch.
func TestDeferredKeyResolvedAtLaunch(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x",
		APIKeyRef: "env:WHATEVER", Model: "m"}
	self := filepath.Join(t.TempDir(), "ak")
	if err := os.WriteFile(self, []byte("#!/usr/bin/env bash\n[ \"$1\" = __key ] && printf 'sk-runtime-%s' \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.sync(t, self)

	data, err := os.ReadFile(filepath.Join(f.bin, "ak-cl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-runtime") {
		t.Fatal("deferred key was written into the command")
	}
	out := run(t, filepath.Join(f.bin, "ak-cl"), nil)
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN=sk-runtime-cl\n") {
		t.Fatalf("key not resolved at launch:\n%s", out)
	}
}

// A tier variant is consumed even when it changes nothing, so it never reaches
// claude as a prompt; with no model configured the alias passes through.
func TestTierVariantAlwaysConsumed(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["same"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKey: "k", Model: "m"}
	f.cfg.Providers["bare"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKey: "k"}
	f.sync(t, "")
	out := run(t, filepath.Join(f.bin, "ak-same"), nil, "opus")
	if !strings.Contains(out, "ANTHROPIC_MODEL=m\n") || strings.Contains(out, "args=opus") {
		t.Fatalf("opus not consumed:\n%s", out)
	}
	out = run(t, filepath.Join(f.bin, "ak-bare"), nil, "haiku")
	if !strings.Contains(out, "ANTHROPIC_MODEL=haiku\n") {
		t.Fatalf("alias not passed through:\n%s", out)
	}
}

// Provider env reaches codex too, and a nested launch drops what the parent
// ak command exported: its proxy, config dir and key do not leak.
func TestProviderEnvAndNestedLaunch(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://x", APIKey: "sk-1",
		CodexHome: "/tmp/cx-home", Env: map[string]string{"HTTPS_PROXY": "http://127.0.0.1:7890"}}
	f.cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y", APIKey: "k", Model: "m"}
	f.sync(t, "")

	out := run(t, filepath.Join(f.bin, "ak-cx"), nil)
	if !strings.Contains(out, "HTTPS_PROXY=http://127.0.0.1:7890\n") || !strings.Contains(out, "CODEX_HOME=/tmp/cx-home\n") {
		t.Fatalf("codex provider env not exported:\n%s", out)
	}
	keys := ""
	for _, ln := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(ln, "AK_ENV_KEYS="); ok {
			keys = v
		}
	}
	if !strings.Contains(keys, "HTTPS_PROXY") || !strings.Contains(keys, "CODEX_HOME") {
		t.Fatalf("AK_ENV_KEYS = %q", keys)
	}

	// ak-cl launched from inside ak-cx.
	parent := []string{"HTTPS_PROXY=http://127.0.0.1:7890", "CODEX_HOME=/tmp/cx-home",
		"AK_KEY_CX=sk-1", "AK_ENV_KEYS=" + keys}
	out = run(t, filepath.Join(f.bin, "ak-cl"), parent)
	for _, leaked := range []string{"HTTPS_PROXY", "CODEX_HOME", "AK_KEY_CX"} {
		if strings.Contains(out, leaked+"=") {
			t.Errorf("%s leaked into the nested launch:\n%s", leaked, out)
		}
	}
	// A proxy the user set themselves is left alone.
	out = run(t, filepath.Join(f.bin, "ak-cl"), []string{"HTTPS_PROXY=http://user:1"})
	if !strings.Contains(out, "HTTPS_PROXY=http://user:1\n") {
		t.Errorf("the user's own proxy was dropped:\n%s", out)
	}
}

// sync tightens the mode of an unchanged command left looser by an older ak.
func TestSyncFixesLooseMode(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y", APIKey: "k", Model: "m"}
	f.sync(t, "")
	path := filepath.Join(f.bin, "ak-cl")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	f.sync(t, "")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != shimMode {
		t.Errorf("mode = %v, want %v", fi.Mode().Perm(), shimMode)
	}
}

// A default variant is applied when the command gets no variant argument, and
// an explicit one still overrides it.
func TestDefaultVariantApplied(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["cx"] = config.Provider{
		Kind: config.KindCodex, BaseURL: "https://x", APIKey: "sk-1", Model: "base",
		DefaultVariant: "high",
	}
	f.sync(t, "")

	out := run(t, filepath.Join(f.bin, "ak-cx"), nil, "-p", "hi")
	if !strings.Contains(out, `-c model_reasoning_effort="high"`) {
		t.Fatalf("default variant not applied:\n%s", out)
	}
	if !strings.Contains(out, "AK_VARIANT=high") {
		t.Fatalf("AK_VARIANT does not name the default:\n%s", out)
	}

	out = run(t, filepath.Join(f.bin, "ak-cx"), nil, "low", "-p", "hi")
	if !strings.Contains(out, `model_reasoning_effort="low"`) || strings.Contains(out, `model_reasoning_effort="high"`) {
		t.Fatalf("explicit variant did not override the default:\n%s", out)
	}
}

// A pool command asks ak to bring the gateway up before it launches, and does
// not launch when that fails; a normal command never asks.
func TestPoolStartsGateway(t *testing.T) {
	f := newSyncFixture(t)
	f.cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKey: "k", Model: "m"}
	f.cfg.Providers["pl"] = config.Provider{Kind: config.KindClaude, Members: []string{"a"}, Model: "m"}
	dir := t.TempDir()
	marker := filepath.Join(dir, "asked")
	self := filepath.Join(dir, "ak")
	body := "#!/usr/bin/env bash\n[ \"$1\" = __gateway-up ] && touch " + marker + " && [ -z \"${FAIL:-}\" ]\n"
	if err := os.WriteFile(self, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	f.sync(t, self)

	run(t, filepath.Join(f.bin, "ak-a"), nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a normal provider asked for the gateway")
	}
	if out := run(t, filepath.Join(f.bin, "ak-pl"), nil); !strings.Contains(out, "args=") {
		t.Fatalf("pool did not launch:\n%s", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("pool command did not ask for the gateway")
	}
	cmd := exec.Command(filepath.Join(f.bin, "ak-pl"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "FAIL=1"}
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "args=") {
		t.Fatalf("pool launched although the gateway did not start:\n%s", out)
	}
}
