package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// The hook goes into every claude settings file in use, including an isolated
// config_dir that has none yet, and doctor checks each of them.
func TestHookAndDoctorCoverConfigDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Providers["iso"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k", ConfigDir: "~/iso"}
	cfg.Providers["cx"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://a", APIKey: "k"}

	paths := claudeSettingsPaths(cfg)
	isolated := filepath.Join(home, "iso", "settings.json")
	if len(paths) != 2 || paths[0] != shared || paths[1] != isolated {
		t.Fatalf("paths = %q", paths)
	}
	if got := residualProviderKeys(shared); len(got) != 1 || got[0] != "ANTHROPIC_BASE_URL" {
		t.Errorf("residual = %q", got)
	}
	if len(hookMissing(cfg)) != 2 {
		t.Fatalf("missing = %q", hookMissing(cfg))
	}
	for _, p := range paths {
		if err := installHook(p); err != nil {
			t.Fatal(err)
		}
	}
	if m := hookMissing(cfg); len(m) != 0 {
		t.Errorf("still missing: %q", m)
	}
	b, _ := os.ReadFile(shared)
	if !strings.Contains(string(b), "ANTHROPIC_BASE_URL") {
		t.Error("install dropped the existing env")
	}
	for _, p := range paths {
		if err := uninstallHook(p); err != nil {
			t.Fatal(err)
		}
	}
	if len(hookMissing(cfg)) != 2 {
		t.Error("uninstall left a hook behind")
	}
}
