package shim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestSyncWritesPiModelsAndShim(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Settings.BinDir = filepath.Join(home, "bin")
	cfg.Providers["relay"] = config.Provider{
		Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1", APIKey: "sk-x",
	}
	piHome := filepath.Join(home, "pi-agent")
	s := &Syncer{Cfg: cfg, PiHome: piHome}

	rep, err := s.Sync()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(home, "bin", "ak-relay")); err != nil {
		t.Fatalf("no command: %v", err)
	}

	// models.json registers the provider and reads the key by interpolation.
	models, err := os.ReadFile(filepath.Join(piHome, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(models, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Providers["ak-relay"].APIKey != "$AK_KEY_RELAY" {
		t.Errorf("models.json entry = %+v", doc.Providers["ak-relay"])
	}

	// A second sync changes nothing.
	rep2, err := s.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if c := rep2.Counts(); c[ActionCreated] != 0 {
		t.Errorf("second sync created %d file(s), want 0", c[ActionCreated])
	}
	_ = rep
}

// A relocated config keeps pi's models.json inside itself: syncing writes
// nothing into the user's ~/.pi.
func TestPiModelsStayInARelocatedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	t.Setenv(config.PiHomeEnv, "")
	cfg := config.Default()
	cfg.Providers["boxpi"] = config.Provider{Kind: config.KindPi, BaseURL: "https://box.example/v1",
		PiAPI: "openai-completions", Model: "m", APIKey: "k"}
	cfg.Resolve()
	// PiHome is left empty on purpose: the syncer must find the relocated one.
	s := &Syncer{Cfg: cfg, CodexHome: t.TempDir(), Self: "/usr/bin/ak"}
	if _, err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pi", "models.json"))
	if err != nil {
		t.Fatalf("models.json was not written inside the config: %v", err)
	}
	if !strings.Contains(string(data), "ak-boxpi") {
		t.Errorf("models.json = %s", data)
	}
}
