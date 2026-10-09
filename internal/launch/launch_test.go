package launch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// fixture is a config whose engines are fake executables.
func fixture(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	for _, name := range []string{"claude", "codex", "pi"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "claude":
			cfg.Settings.ClaudeBin = p
		case "codex":
			cfg.Settings.CodexBin = p
		case "pi":
			cfg.Settings.PiBin = p
		}
	}
	return cfg
}

func build(t *testing.T, cfg *config.Config, target string, o Options, args ...string) Plan {
	t.Helper()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	name, variant, err := ParseTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Build(cfg, name, variant, args, o)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func envOf(p Plan) map[string]string { return envMap(p.Env) }

func argsOf(p Plan) string { return strings.Join(p.Argv[1:], " ") }

func TestParseTarget(t *testing.T) {
	cases := map[string][2]string{"kimi": {"kimi", ""}, "kimi:high": {"kimi", "high"}}
	for in, want := range cases {
		n, v, err := ParseTarget(in)
		if err != nil || n != want[0] || v != want[1] {
			t.Errorf("ParseTarget(%q) = %q, %q, %v", in, n, v, err)
		}
	}
	for _, bad := range []string{"", ":high", "kimi:"} {
		if _, _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q) accepted", bad)
		}
	}
}

func TestCodexVariants(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["cx"] = config.Provider{
		Kind: config.KindCodex, BaseURL: "https://x", APIKey: "sk-1", Model: "base",
		Variants: map[string]config.Variant{"fast": {Model: "gpt-fast", Reasoning: "low"}},
	}
	p := build(t, cfg, "cx:fast", Options{Key: "sk-1"}, "hello")
	if a := argsOf(p); !strings.Contains(a, `-c model="gpt-fast" -c model_reasoning_effort="low" hello`) ||
		!strings.Contains(a, `model_provider="cx"`) {
		t.Fatalf("args = %s", a)
	}
	if env := envOf(p); env["AK_VARIANT"] != "fast" || env["AK_KEY_CX"] != "sk-1" {
		t.Fatalf("env = %v", env)
	}
	p = build(t, cfg, "cx:high", Options{})
	if !strings.Contains(argsOf(p), `-c model_reasoning_effort="high"`) {
		t.Fatalf("reasoning variant args = %s", argsOf(p))
	}
	// A first argument that happens to name a variant is the engine's, not ak's.
	p = build(t, cfg, "cx", Options{}, "high")
	if a := argsOf(p); strings.Contains(a, "model_reasoning_effort") || !strings.HasSuffix(a, " high") {
		t.Fatalf("positional variant consumed: %s", a)
	}
	if _, err := Build(cfg, "cx", "nope", nil, Options{}); err == nil || !strings.Contains(err.Error(), "available") {
		t.Fatalf("unknown variant: %v", err)
	}
}

// A default variant applies unless the target names another.
func TestDefaultVariant(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://x", APIKey: "k", DefaultVariant: "high"}
	p := build(t, cfg, "cx", Options{}, "-p", "hi")
	if !strings.Contains(argsOf(p), `model_reasoning_effort="high"`) || envOf(p)["AK_VARIANT"] != "high" {
		t.Fatalf("default not applied: %s", argsOf(p))
	}
	p = build(t, cfg, "cx:low", Options{})
	if a := argsOf(p); !strings.Contains(a, `"low"`) || strings.Contains(a, `"high"`) {
		t.Fatalf("explicit variant did not override the default: %s", a)
	}
}

// A parent's variant and provider environment do not leak into a nested
// launch; a variable the user set themselves stays.
func TestNestedLaunch(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://x", APIKey: "sk-1",
		CodexHome: "/tmp/cx-home", Env: map[string]string{"HTTPS_PROXY": "http://127.0.0.1:7890"}}
	cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y", APIKey: "k", Model: "m", Opus: "big"}

	parent := envOf(build(t, cfg, "cx", Options{Key: "sk-1"}))
	if !strings.Contains(parent[EnvKeys], "HTTPS_PROXY") || !strings.Contains(parent[EnvKeys], "CODEX_HOME") {
		t.Fatalf("%s = %q", EnvKeys, parent[EnvKeys])
	}
	environ := []string{"PATH=/usr/bin", "AK_VARIANT=opus", "ANTHROPIC_API_KEY=stale"}
	for k, v := range parent {
		environ = append(environ, k+"="+v)
	}
	env := envOf(build(t, cfg, "cl", Options{Environ: environ, Key: "k"}))
	for _, leaked := range []string{"HTTPS_PROXY", "CODEX_HOME", "AK_KEY_CX", "AK_VARIANT", "ANTHROPIC_API_KEY"} {
		if _, ok := env[leaked]; ok {
			t.Errorf("%s leaked into the nested launch", leaked)
		}
	}
	if env["ANTHROPIC_MODEL"] != "m" || env["ANTHROPIC_AUTH_TOKEN"] != "k" || env["PATH"] != "/usr/bin" {
		t.Fatalf("env = %v", env)
	}
	env = envOf(build(t, cfg, "cl", Options{Environ: []string{"HTTPS_PROXY=http://user:1"}}))
	if env["HTTPS_PROXY"] != "http://user:1" {
		t.Errorf("the user's own proxy was dropped: %v", env)
	}
	if env := envOf(build(t, cfg, "cl:opus", Options{})); env["ANTHROPIC_MODEL"] != "big" {
		t.Errorf("opus variant: %v", env)
	}
}

// The engine binary override wins over settings; a missing engine says how to fix it.
func TestFindBin(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y", APIKey: "k"}
	other := filepath.Join(t.TempDir(), "claude2")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p := build(t, cfg, "cl", Options{Environ: []string{"AK_CLAUDE_BIN=" + other}}); p.Bin != other || p.Argv[0] != other {
		t.Fatalf("bin = %s", p.Bin)
	}
	cfg.Settings.ClaudeBin = filepath.Join(t.TempDir(), "missing")
	t.Setenv("PATH", t.TempDir())
	if _, err := Build(cfg, "cl", "", nil, Options{}); err == nil || !strings.Contains(err.Error(), "AK_CLAUDE_BIN") {
		t.Fatalf("missing engine: %v", err)
	}
}

func TestPoolFlag(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKey: "k", Model: "m"}
	cfg.Providers["pl"] = config.Provider{Kind: config.KindClaude, Members: []string{"a"}, Model: "m"}
	if build(t, cfg, "a", Options{}).Pool || !build(t, cfg, "pl", Options{}).Pool {
		t.Fatal("pool flag wrong")
	}
	if env := envOf(build(t, cfg, "pl", Options{})); !strings.HasSuffix(env["ANTHROPIC_BASE_URL"], "/p/pl") {
		t.Fatalf("pool not routed to the gateway: %v", env)
	}
}

// claude gets ak's session id only when it starts a fresh session.
func TestClaudeSessionID(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["cl"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y", APIKey: "k"}
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://x", APIKey: "k"}
	o := Options{SessionID: "11111111-1111-4111-8111-111111111111"}
	p := build(t, cfg, "cl", o, "-p", "hi")
	if p.SessionID != o.SessionID || !strings.Contains(argsOf(p), "--session-id "+o.SessionID+" -p hi") {
		t.Fatalf("session id not passed: %s", argsOf(p))
	}
	for _, args := range [][]string{{"--resume"}, {"-c"}, {"--session-id=x"}, {"mcp", "list"}, {"--fork-session", "-r", "x"}} {
		if p := build(t, cfg, "cl", o, args...); p.SessionID != "" || strings.Contains(argsOf(p), o.SessionID) {
			t.Errorf("%v: session id passed", args)
		}
	}
	if p := build(t, cfg, "cx", o); p.SessionID != "" {
		t.Error("codex got a claude session id")
	}
}

func TestNewSessionID(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(id) {
		t.Fatalf("not a v4 UUID: %s", id)
	}
}

// pi selects the ak-managed provider and carries the key in the environment,
// where models.json reads it.
func TestPiLaunch(t *testing.T) {
	cfg := fixture(t)
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1", APIKey: "sk-x"}
	p := build(t, cfg, "relay:high", Options{Key: "sk-x"})
	if a := argsOf(p); a != "--provider ak-relay --model m1 --thinking high" {
		t.Fatalf("args = %s", a)
	}
	if envOf(p)["AK_KEY_RELAY"] != "sk-x" {
		t.Fatalf("key not exported: %v", p.Env)
	}
}
