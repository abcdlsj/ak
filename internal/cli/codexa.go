package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/pelletier/go-toml/v2"
)

// parseCodexaProfile extracts provider settings from a codexa profile's
// config.toml.
//
// Read-only by design. A codexa profile directory holds GB-sized log
// databases, some of them symlinked back to the primary home; following or
// deleting those would cause irreversible damage, so nothing here writes.
func parseCodexaProfile(raw []byte) (codexaProfile, error) {
	var doc struct {
		ModelProvider   string `toml:"model_provider"`
		Model           string `toml:"model"`
		ReasoningEffort string `toml:"model_reasoning_effort"`
		ModelProviders  map[string]struct {
			BaseURL string `toml:"base_url"`
			WireAPI string `toml:"wire_api"`
			EnvKey  string `toml:"env_key"`
			Name    string `toml:"name"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return codexaProfile{}, err
	}

	if doc.ModelProvider == "" {
		return codexaProfile{}, nil
	}
	table, ok := doc.ModelProviders[doc.ModelProvider]
	if !ok {
		return codexaProfile{}, fmt.Errorf("model_provider %q has no matching [model_providers] table", doc.ModelProvider)
	}

	out := codexaProfile{
		Model:      doc.Model,
		Reasoning:  doc.ReasoningEffort,
		ProviderID: doc.ModelProvider,
		WireAPI:    "responses",
		EnvKey:     table.EnvKey,
		BaseURL:    table.BaseURL,
	}
	if table.WireAPI != "" {
		out.WireAPI = table.WireAPI
	}

	// Drop invalid reasoning values outright; they would fail validation.
	if out.Reasoning != "" && !config.ValidReasoning()[out.Reasoning] {
		out.Reasoning = ""
	}
	return out, nil
}

// codexaKey reads the API key from a profile's auth.json, keyed by env_key.
// On failure it returns an empty string so the caller can report that a key
// is still needed without aborting the import.
func codexaKey(profileDir, envKey string) string {
	if envKey == "" {
		envKey = "OPENAI_API_KEY"
	}
	data, err := os.ReadFile(filepath.Join(profileDir, "auth.json"))
	if err != nil {
		return ""
	}
	var auth map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}
	if k, ok := auth[envKey]; ok && k != "" {
		return k
	}
	// Fallback: when the key named by env_key is absent, take the first
	// non-empty value.
	for _, v := range auth {
		if v != "" {
			return v
		}
	}
	return ""
}
