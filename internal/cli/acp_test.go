package cli

import (
	"os"
	"os/exec"
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
	server := acpServers[config.KindClaude]
	t.Setenv(acpBinEnv(config.KindClaude), bin)
	if got, err := findACPBin(config.KindClaude, server); err != nil || len(got) != 1 || got[0] != bin {
		t.Fatalf("findACPBin = %q, %v; want the override", got, err)
	}
	t.Setenv(acpBinEnv(config.KindClaude), filepath.Join(t.TempDir(), "missing"))
	if _, err := findACPBin(config.KindClaude, server); err == nil {
		t.Fatal("a non-executable override was accepted")
	}
}

func TestACPIsUnavailableForEnginesWithoutAnAdapter(t *testing.T) {
	if _, ok := acpServers[config.KindCodex]; ok {
		t.Fatal("codex is listed although its adapter cannot read the provider environment")
	}
}

func TestACPPiFallsBackToNpxWhenTheAdapterIsNotInstalled(t *testing.T) {
	// Hide the adapter and keep npx reachable, so the fallback is what resolves.
	t.Setenv("PATH", "/usr/bin:/bin")
	if _, err := exec.LookPath("pi-acp"); err == nil {
		t.Skip("pi-acp is installed on this machine")
	}
	t.Setenv(acpBinEnv(config.KindPi), "")
	got, err := findACPBin(config.KindPi, acpServers[config.KindPi])
	if _, lookErr := exec.LookPath("npx"); lookErr != nil {
		if err == nil {
			t.Fatalf("without npx the adapter must be reported missing, got %v", got)
		}
		return
	}
	if err != nil || len(got) != 3 || got[2] != "pi-acp@0.0.34" {
		t.Fatalf("findACPBin = %q, %v; want the npx fallback", got, err)
	}
}

func TestACPPiProviderEnvPinsProviderAndModel(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["od"] = config.Provider{
		Kind: config.KindPi, BaseURL: "https://example.test/v1", Model: "deepseek-v4.1-flash", APIKey: "sk-x",
	}
	env, err := acpProviderEnv(cfg, config.KindPi, "od", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		values[key] = value
	}
	// ak registers its providers in pi's catalog under an ak- prefix.
	if values["PI_PROVIDER"] != "ak-od" {
		t.Fatalf("PI_PROVIDER = %q, want the plan's pi provider name", values["PI_PROVIDER"])
	}
	if values["PI_MODEL"] != "deepseek-v4.1-flash" {
		t.Fatalf("PI_MODEL = %q, want the provider's model", values["PI_MODEL"])
	}
}

func TestACPProviderFlagBeatsResolution(t *testing.T) {
	cmd := newACPCmd()
	flag := cmd.Flags().Lookup("provider")
	if flag == nil {
		t.Fatal("acp has no --provider flag")
	}
	if err := cmd.ParseFlags([]string{"--provider", "od"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cmd.Flags().GetString("provider"); got != "od" {
		t.Fatalf("--provider = %q", got)
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
