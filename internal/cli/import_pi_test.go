package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestKeyRefOrLiteral(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"sk-abc":                    "sk-abc",
		"$MY_KEY":                   "env:MY_KEY",
		"${MY_KEY}":                 "env:MY_KEY",
		"!security find-generic-pw": "cmd:security find-generic-pw",
	}
	for in, want := range cases {
		if got := keyRefOrLiteral(in); got != want {
			t.Errorf("keyRefOrLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPiProviderFrom(t *testing.T) {
	p, ok := piProviderFrom("x", piModelProvider{BaseURL: "https://relay.example", APIKey: "$K", API: "openai-completions"})
	if !ok || p.BaseURL != "https://relay.example" || p.APIKeyRef != "env:K" || p.PiAPI != "openai-completions" {
		t.Fatalf("provider = %+v ok=%v", p, ok)
	}

	// No base URL: it customizes a provider pi already has, kept by id.
	builtin := piModelProvider{Models: []struct {
		ID string `json:"id"`
	}{{ID: "m"}}}
	builtin.Name = "builtin"
	p, ok = piProviderFrom("builtin", builtin)
	if !ok || p.PiProvider != "builtin" || p.Model != "m" {
		t.Fatalf("provider = %+v ok=%v", p, ok)
	}

	if _, ok := piProviderFrom("x", piModelProvider{}); ok {
		t.Error("an empty entry was imported")
	}
}

func TestImportPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	piDir := filepath.Join(home, "pi")
	t.Setenv("PI_CODING_AGENT_DIR", piDir)
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := `{"providers":{
	  "myrelay":{"baseUrl":"https://relay.example","apiKey":"$MY_KEY","api":"openai-completions","models":[{"id":"m1"}]},
	  "ak-mine":{"baseUrl":"https://x","models":[{"id":"m0"}]},
	  "builtin":{"apiKey":"literal","models":[{"id":"m2"}]}
	}}`
	if err := os.WriteFile(filepath.Join(piDir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Settings.BinDir = filepath.Join(home, "bin")
	if err := importPi(cfg); err != nil {
		t.Fatal(err)
	}

	// The config was saved; reload it as the user's next command would.
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Providers["ak-mine"]; ok {
		t.Error("an ak-owned provider was imported")
	}
	relay, ok := got.Providers["myrelay"]
	if !ok {
		t.Fatalf("myrelay was not imported: %v", got.Names())
	}
	if relay.Kind != config.KindPi || relay.BaseURL != "https://relay.example" || relay.APIKeyRef != "env:MY_KEY" || relay.Model != "m1" {
		t.Errorf("relay = %+v", relay)
	}
	builtin, ok := got.Providers["builtin"]
	if !ok {
		t.Fatalf("builtin was not imported: %v", got.Names())
	}
	if builtin.PiProvider != "builtin" || builtin.Model != "m2" {
		t.Errorf("builtin = %+v", builtin)
	}
}
