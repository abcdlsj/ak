package cli

import (
	"fmt"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/pelletier/go-toml/v2"
)

// codexProfile is the settings one codex config.toml declares: the provider it
// picks and that provider's endpoint. cc-switch stores a codex profile as a
// TOML string in its database, so this is how such an entry is read.
type codexProfile struct {
	ProviderID string
	BaseURL    string
	WireAPI    string
	Model      string
	Reasoning  string
	EnvKey     string
}

// parseCodexProfile extracts provider settings from a codex config.toml.
func parseCodexProfile(raw []byte) (codexProfile, error) {
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
		return codexProfile{}, err
	}

	if doc.ModelProvider == "" {
		return codexProfile{}, nil
	}
	table, ok := doc.ModelProviders[doc.ModelProvider]
	if !ok {
		return codexProfile{}, fmt.Errorf("model_provider %q has no matching [model_providers] table", doc.ModelProvider)
	}

	out := codexProfile{
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
