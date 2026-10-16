package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestACPProviderEnvInjectsProviderAndPinsEngineBin(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AK_CLAUDE_BIN", bin)
	cfg := config.Default()
	cfg.Providers["bili"] = config.Provider{
		Kind: config.KindClaude, BaseURL: "https://example.test", APIKey: "sk-x",
	}
	env, err := acpProviderEnv(cfg, config.KindClaude, "bili", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		values[key] = value
	}
	if values["ANTHROPIC_BASE_URL"] != "https://example.test" || values["ANTHROPIC_AUTH_TOKEN"] != "sk-x" {
		t.Fatalf("provider environment missing: %v", values)
	}
	// The adapter starts the engine itself, so it must start the binary this
	// provider was configured with.
	if values["CLAUDE_CODE_EXECUTABLE"] != bin {
		t.Fatalf("CLAUDE_CODE_EXECUTABLE = %q, want %q", values["CLAUDE_CODE_EXECUTABLE"], bin)
	}
}

func TestACPProviderEnvRequiresAKey(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["nokey"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://example.test"}
	if _, err := acpProviderEnv(cfg, config.KindClaude, "nokey", os.Environ()); err == nil {
		t.Fatal("a provider without a key was accepted")
	}
}

func TestFindACPBinUsesOverrideThenPath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "adapter")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(acpBinEnv(config.KindClaude), bin)
	if got, err := findACPBin(config.KindClaude, "claude-agent-acp"); err != nil || got != bin {
		t.Fatalf("findACPBin = %q, %v; want the override", got, err)
	}
	t.Setenv(acpBinEnv(config.KindClaude), filepath.Join(t.TempDir(), "missing"))
	if _, err := findACPBin(config.KindClaude, "claude-agent-acp"); err == nil {
		t.Fatal("a non-executable override was accepted")
	}
}

func TestACPIsUnavailableForEnginesWithoutAnAdapter(t *testing.T) {
	if _, ok := acpServers[config.KindCodex]; ok {
		t.Fatal("codex is listed although its adapter cannot read the provider environment")
	}
	if _, ok := acpServers[config.KindPi]; ok {
		t.Fatal("pi has no ACP server")
	}
}

func TestWithEnvReplacesInPlace(t *testing.T) {
	got := withEnv([]string{"A=1", "B=2"}, "A", "3")
	if len(got) != 2 || got[0] != "A=3" || got[1] != "B=2" {
		t.Fatalf("withEnv = %v", got)
	}
	if got := withEnv([]string{"A=1"}, "B", "2"); len(got) != 2 || got[1] != "B=2" {
		t.Fatalf("withEnv append = %v", got)
	}
}
