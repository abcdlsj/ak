package shim

import (
	"encoding/json"
	"os"
	"path/filepath"
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
