package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
)

// piModelsFile is the part of pi's models.json ak reads for an import.
type piModelsFile struct {
	Providers map[string]piModelProvider `json:"providers"`
}

type piModelProvider struct {
	Name       string `json:"name"`
	BaseURL    string `json:"baseUrl"`
	APIKey     string `json:"apiKey"`
	API        string `json:"api"`
	AuthHeader bool   `json:"authHeader"`
	Models     []struct {
		ID string `json:"id"`
	} `json:"models"`
}

// piAgentDir is where pi keeps its configuration.
func piAgentDir() string {
	if d := os.Getenv(provider.EnvPiHome); d != "" {
		return config.ExpandHome(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// importPi copies the providers from pi's models.json into ak. Providers ak
// itself registered (ak-*) are skipped, and so is a provider with neither an
// endpoint nor a model: there would be nothing to run.
func importPi(cfg *config.Config) error {
	dir := piAgentDir()
	if dir == "" {
		return fmt.Errorf("cannot locate pi's agent directory")
	}
	path := filepath.Join(dir, "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var file piModelsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if len(file.Providers) == 0 {
		fmt.Printf("%s declares no providers.\n", path)
		return nil
	}

	used := map[string]bool{}
	added := 0
	for id, src := range file.Providers {
		if strings.HasPrefix(id, provider.PiProviderPrefix) {
			continue // ak's own entry
		}
		p, ok := piProviderFrom(id, src)
		if !ok {
			fmt.Printf("  skip  %s: no base URL, model or existing pi provider\n", id)
			continue
		}
		base := slugify(id)
		name := uniqueProviderName(cfg, used, base, id)
		used[name] = true
		if err := config.ValidateName(name); err != nil {
			fmt.Printf("  skip  %s: %v\n", id, err)
			continue
		}
		if err := core.Add(cfg, name, p); err != nil {
			fmt.Printf("  skip  %s: %v\n", id, err)
			continue
		}
		fmt.Printf("  added %s (pi provider %s)\n", name, id)
		added++
	}
	if added == 0 {
		fmt.Println("Nothing to import.")
		return nil
	}
	return runSync(cfg, false)
}

// piProviderFrom turns one models.json entry into an ak pi provider. A missing
// base URL means the entry customizes a provider pi already has, so ak keeps
// naming it (pi_provider) rather than registering a new one.
func piProviderFrom(id string, src piModelProvider) (config.Provider, bool) {
	p := config.Provider{Kind: config.KindPi, PiAPI: src.API, PiAuthHeader: src.AuthHeader, Display: src.Name}
	if len(src.Models) > 0 {
		p.Model = src.Models[0].ID
	}
	if src.BaseURL == "" && p.Model == "" {
		return config.Provider{}, false
	}
	if src.BaseURL == "" {
		// Overrides a built-in provider: keep using that provider by id.
		p.PiProvider = id
		return p, true
	}
	p.BaseURL = src.BaseURL
	p.SetKey(keyRefOrLiteral(src.APIKey))
	return p, true
}

// keyRefOrLiteral turns an imported key into ak's form: an environment
// interpolation becomes env:NAME, a command becomes cmd:..., and anything else
// is kept as a literal.
func keyRefOrLiteral(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "!"):
		return "cmd:" + strings.TrimPrefix(v, "!")
	case strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}"):
		return "env:" + v[2:len(v)-1]
	case strings.HasPrefix(v, "$"):
		return "env:" + v[1:]
	default:
		return v
	}
}
