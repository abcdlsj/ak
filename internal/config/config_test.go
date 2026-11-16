package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnknownKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := Path()
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("version = 1\n[providers.a]\nkind = 'claude'\nbase_url = 'x'\nmodle = 'm'\n"), 0o600)
	keys, err := UnknownKeys()
	if err != nil || !reflect.DeepEqual(keys, []string{"providers.a.modle"}) {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
}

// AK_CONFIG_DIR replaces ~/.config/ak for every file ak keeps there, so a
// process started under another HOME still reaches the user's providers.
func TestConfigDirOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)
	if got, err := Path(); err != nil || got != filepath.Join(dir, "providers.toml") {
		t.Fatalf("Path() = %q, %v", got, err)
	}
	if got, err := AuthPath(); err != nil || got != filepath.Join(dir, "auth.json") {
		t.Fatalf("AuthPath() = %q, %v", got, err)
	}
	t.Setenv(DirEnv, "relative/ak")
	if _, err := Dir(); err == nil {
		t.Fatal("a relative AK_CONFIG_DIR was accepted")
	}
}

// A default_variant must name a variant the provider's command recognises.
func TestValidateDefaultVariant(t *testing.T) {
	ok := []Provider{
		{Kind: KindPi, BaseURL: "https://x", Model: "m", DefaultVariant: "high"},
		{Kind: KindCodex, BaseURL: "https://x", DefaultVariant: "xhigh"},
		{Kind: KindClaude, BaseURL: "https://x", Model: "m", DefaultVariant: "opus"},
		{Kind: KindPi, BaseURL: "https://x", Model: "m", DefaultVariant: "fast",
			Variants: map[string]Variant{"fast": {Model: "m2"}}},
		{Kind: KindClaude, BaseURL: "https://x", Model: "m", DefaultVariant: "unsure",
			Variants: map[string]Variant{"unsure": {Model: "m2"}}},
	}
	for i, p := range ok {
		if err := Validate(piCfg(p)); err != nil {
			t.Errorf("case %d rejected: %v", i, err)
		}
	}

	bad := []Provider{
		{Kind: KindPi, BaseURL: "https://x", Model: "m", DefaultVariant: "fast"},     // unknown for pi
		{Kind: KindCodex, BaseURL: "https://x", DefaultVariant: "off"},               // pi level, not codex
		{Kind: KindClaude, BaseURL: "https://x", Model: "m", DefaultVariant: "high"}, // reasoning level, not a tier
	}
	for i, p := range bad {
		if err := Validate(piCfg(p)); err == nil {
			t.Errorf("case %d accepted an unknown default_variant", i)
		}
	}
}

// pi's models.json lives where pi reads it: PI_CODING_AGENT_DIR when set,
// else ~/.pi/agent — and inside the config directory when that is relocated,
// so a run against a config of its own writes nothing outside it.
func TestPiHome(t *testing.T) {
	t.Setenv(PiHomeEnv, "")
	t.Setenv(DirEnv, "")
	home, _ := os.UserHomeDir()
	if got, err := PiHome(); err != nil || got != filepath.Join(home, ".pi", "agent") {
		t.Errorf("PiHome = %q, %v; want the user's", got, err)
	}
	if PiHomeSandboxed() {
		t.Error("the user's home reported as sandboxed")
	}

	t.Setenv(DirEnv, t.TempDir())
	if got, _ := PiHome(); got != filepath.Join(os.Getenv(DirEnv), "pi") {
		t.Errorf("PiHome = %q, want a directory inside the config", got)
	}
	if !PiHomeSandboxed() {
		t.Error("a relocated config is sandboxed, so pi must be told where to look")
	}

	// An explicit pi home is the user's own choice and wins over the sandbox.
	t.Setenv(PiHomeEnv, "~/pi-of-mine")
	if got, _ := PiHome(); got != filepath.Join(home, "pi-of-mine") {
		t.Errorf("PiHome = %q, want the explicit one", got)
	}
	if PiHomeSandboxed() {
		t.Error("an explicit pi home needs no export")
	}
}
