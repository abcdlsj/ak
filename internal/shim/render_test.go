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
		"env | grep -E '^(AK_|ANTHROPIC_)' | sort\n"
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
	if !strings.Contains(out, `args=--profile ak-cx -c model="gpt-fast" -c model_reasoning_effort="low" hello`) {
		t.Fatalf("custom variant args wrong:\n%s", out)
	}
	if strings.Contains(out, `model_reasoning_effort="fast"`) {
		t.Fatalf("custom variant name leaked as a reasoning level:\n%s", out)
	}
	out = run(t, filepath.Join(f.bin, "ak-cx"), nil, "high")
	if !strings.Contains(out, `args=--profile ak-cx -c model_reasoning_effort="high"`) {
		t.Fatalf("reasoning variant args wrong:\n%s", out)
	}
	// The standalone variant command behaves like `ak-cx fast`.
	out = run(t, filepath.Join(f.bin, "ak-cx-fast"), nil, "hi")
	if !strings.Contains(out, `-c model="gpt-fast"`) || !strings.Contains(out, "AK_VARIANT=fast") {
		t.Fatalf("alias output wrong:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.codexHome, "ak-cx.config.toml")); err != nil {
		t.Fatalf("codex profile not written: %v", err)
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
