package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBase(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadBaseCodexProviderIDsMissingConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if ids := readBaseCodexProviderIDs(); len(ids) != 0 {
		t.Fatalf("ids = %v, want empty when there is no base config", ids)
	}
}

func TestReadBaseCodexProviderIDsSelectedAndDeclared(t *testing.T) {
	writeBase(t, `
model_provider = "custom"

[model_providers.custom]
base_url = "https://example.com"

[model_providers.spare]
base_url = "https://spare.example.com"
`)
	ids := readBaseCodexProviderIDs()
	for _, want := range []string{"custom", "spare"} {
		if !ids[want] {
			t.Errorf("ids = %v, want it to contain %q", ids, want)
		}
	}
}
