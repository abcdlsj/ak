package provider

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// baseCodexIDs caches the base config's provider ids for the process.
var baseCodexIDs struct {
	once sync.Once
	ids  map[string]bool
}

// BaseCodexProviderIDs returns the model_provider ids declared by the user's
// own ~/.codex/config.toml.
//
// These are the engine's default providers, and codex records only the id in
// a session log. An ak provider configured with the same id is therefore
// indistinguishable from the default one, so the id stays unmapped and usage
// is reported under the raw id instead of being credited to one of them.
func BaseCodexProviderIDs() map[string]bool {
	baseCodexIDs.once.Do(func() { baseCodexIDs.ids = readBaseCodexProviderIDs() })
	return baseCodexIDs.ids
}

// readBaseCodexProviderIDs parses the base config, returning an empty set when
// it is missing or unreadable: no base config means nothing can collide.
func readBaseCodexProviderIDs() map[string]bool {
	ids := map[string]bool{}
	data, err := os.ReadFile(BaseCodexConfigPath())
	if err != nil {
		return ids
	}
	var doc struct {
		ModelProvider  string `toml:"model_provider"`
		ModelProviders map[string]struct {
			BaseURL string `toml:"base_url"`
		} `toml:"model_providers"`
	}
	if toml.Unmarshal(data, &doc) != nil {
		return ids
	}
	// A provider can be declared without being selected, and is still usable
	// through -c model_provider=..., so a declared id can collide too.
	if doc.ModelProvider != "" {
		ids[doc.ModelProvider] = true
	}
	for id := range doc.ModelProviders {
		ids[id] = true
	}
	return ids
}

// BaseCodexConfigPath is ~/.codex/config.toml, the config an ak-* codex
// command layers its profile over.
func BaseCodexConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}
