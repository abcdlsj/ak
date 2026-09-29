package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestPiLaunchExistingProvider(t *testing.T) {
	p := config.Provider{Kind: config.KindPi, PiProvider: "commandcode", Model: "deepseek/deepseek-v4.1-flash"}
	l, err := piEngine{}.Launch("cc", p, Literal("unused"), Context{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Args, " "); got != "--provider commandcode --model deepseek/deepseek-v4.1-flash" {
		t.Errorf("args = %q", got)
	}
	env := envMap(l.Env)
	if _, ok := env[EnvKey("cc")]; ok {
		t.Error("a key was exported for a provider that names an existing pi provider")
	}
	if env["AK_PROVIDER"] != "cc" {
		t.Errorf("AK_PROVIDER = %q", env["AK_PROVIDER"])
	}
}

func TestPiLaunchManagedProvider(t *testing.T) {
	p := config.Provider{Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1"}
	l, err := piEngine{}.Launch("relay", p, Literal("sk-secret"), Context{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Args, " "); got != "--provider ak-relay --model m1" {
		t.Errorf("args = %q", got)
	}
	if envMap(l.Env)[EnvKey("relay")] != "sk-secret" {
		t.Error("the key was not exported for the models.json interpolation")
	}
}

func TestPiVariants(t *testing.T) {
	p := config.Provider{Kind: config.KindPi, BaseURL: "https://x", Model: "m1"}
	l, err := piEngine{}.Launch("x", p, Literal("k"), Context{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Variant{}
	for _, v := range l.Variants {
		byName[v.Name] = v
	}
	if got := strings.Join(byName["high"].Args, " "); got != "--thinking high" {
		t.Errorf("high variant args = %q", got)
	}
}

func TestPiPoolPointsAtGateway(t *testing.T) {
	p := config.Provider{Kind: config.KindPi, Members: []string{"a", "b"}, Model: "m"}
	l, err := piEngine{}.Launch("pool", p, Literal("member-key-must-not-appear"), Context{Gateway: testGateway})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Args, " "); got != "--provider ak-pool --model m" {
		t.Errorf("args = %q", got)
	}
	if envMap(l.Env)[EnvKey("pool")] != poolKey {
		t.Error("pool did not use the placeholder key")
	}
}

func TestPiModelsJSONMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	// A user's own file, with formatting and a provider ak must not touch.
	user := `{
  "providers": {
    "ollama": { "baseUrl": "http://localhost:11434/v1", "api": "openai-completions" }
  }
}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1", PiAuthHeader: true}
	cfg.Providers["pinned"] = config.Provider{Kind: config.KindPi, PiProvider: "commandcode", Model: "m"}
	cfg.Providers["claude"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a"}

	outPath, content, err := PiModelsJSON(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if outPath != path {
		t.Fatalf("path = %q", outPath)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL    string `json:"baseUrl"`
			APIKey     string `json:"apiKey"`
			AuthHeader bool   `json:"authHeader"`
			Models     []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, content)
	}
	if _, ok := doc.Providers["ollama"]; !ok {
		t.Error("the user's provider was dropped")
	}
	entry, ok := doc.Providers["ak-relay"]
	if !ok {
		t.Fatal("ak-relay was not added")
	}
	if entry.BaseURL != "https://relay.example" || entry.APIKey != "$AK_KEY_RELAY" || !entry.AuthHeader {
		t.Errorf("entry = %+v", entry)
	}
	if len(entry.Models) != 1 || entry.Models[0].ID != "m1" {
		t.Errorf("models = %+v", entry.Models)
	}
	if _, ok := doc.Providers["ak-pinned"]; ok {
		t.Error("a provider that names an existing pi provider was registered anyway")
	}
	if _, ok := doc.Providers["ak-claude"]; ok {
		t.Error("a non-pi provider was registered in models.json")
	}
}

func TestPiModelsJSONIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1"}

	_, first, err := PiModelsJSON(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), first, 0o644); err != nil {
		t.Fatal(err)
	}
	_, second, err := PiModelsJSON(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("a second run changed the file:\n%s\n---\n%s", first, second)
	}
}

func TestPiModelsJSONUnchangedLeavesUserBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	// ak has one entry already; the user's formatting has no trailing newline.
	user := `{"providers":{"ak-relay":{"apiKey":"$AK_KEY_RELAY","baseUrl":"https://relay.example","models":[{"id":"m1"}],"name":"relay"},"mine":{"baseUrl":"https://x"}}}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1"}

	_, content, err := PiModelsJSON(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != user {
		t.Errorf("an unchanged file was reformatted:\n%s", content)
	}
}

func TestPiModelsJSONReclaimsRemovedProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://relay.example", Model: "m1"}
	_, content, err := PiModelsJSON(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	// The provider is gone: its entry must be reclaimed.
	_, content, err = PiModelsJSON(dir, config.Default(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "ak-relay") {
		t.Errorf("removed provider left behind:\n%s", content)
	}
}

func TestPiModelsJSONRefusesBadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://x", Model: "m"}
	if _, _, err := PiModelsJSON(dir, cfg, ""); err == nil {
		t.Fatal("a malformed models.json was overwritten")
	}
}

func TestPiModelsJSONPoolPointsAtGateway(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Providers["pool"] = config.Provider{Kind: config.KindPi, Members: []string{"a", "b"}, Model: "logical"}
	_, content, err := PiModelsJSON(dir, cfg, testGateway)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Providers["ak-pool"].BaseURL; got != testGateway+"/p/pool" {
		t.Errorf("pool baseUrl = %q, want the gateway", got)
	}
}

func TestPiModelsJSONMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	path, content, err := PiModelsJSON(dir, config.Default(), "")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" || content != nil {
		t.Errorf("nothing to do should return empty, got %q", path)
	}
}
