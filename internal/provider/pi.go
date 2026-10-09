package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// PiProviderPrefix namespaces the providers ak registers in pi's models.json,
// so syncing can tell its own entries from the user's and never removes theirs.
const PiProviderPrefix = "ak-"

// EnvPiHome is the variable pi reads to relocate its agent directory; ak
// honours it when deciding where models.json lives.
const EnvPiHome = "PI_CODING_AGENT_DIR"

// PiProviderID is the pi provider id ak registers for a provider.
func PiProviderID(name string) string { return PiProviderPrefix + name }

// piEngine runs the pi coding agent. A provider either names a pi provider pi
// already knows (pi_provider) and writes nothing, or gets an ak-<name> entry in
// pi's models.json that ak keeps in sync.
type piEngine struct{}

func (piEngine) Kind() config.Kind                      { return config.KindPi }
func (piEngine) BinName() string                        { return "pi" }
func (piEngine) BinEnvVar() string                      { return "AK_PI_BIN" }
func (piEngine) ConfiguredBin(s config.Settings) string { return s.PiBin }

// ArtifactDirs is nil: pi's only ak-owned file is models.json, which Sync
// writes through Shared and which must never be reclaimed as an orphan.
func (piEngine) ArtifactDirs(Context) []string { return nil }

// Launch builds the command: --provider selects the pi provider, --model the
// model when ak knows one. The key travels in AK_KEY_<NAME>, which models.json
// reads by interpolation, so it never lands on a command line or in a file.
func (piEngine) Launch(name string, p config.Provider, key Secret, ctx Context) (Launch, error) {
	if p.IsPool() && ctx.Gateway != "" {
		p = poolTarget(p, name, ctx.Gateway)
		key = Literal(poolKey)
	}

	env := newEnv()
	providerID := p.PiProvider
	if providerID == "" {
		providerID = PiProviderID(name)
		env.setSecret(EnvKey(name), key)
	}
	env.set("AK_PROVIDER", name)
	// Extra env vars merge last, as for claude.
	for k, v := range p.Env {
		env.set(k, v)
	}

	args := []string{"--provider", providerID}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	l := Launch{Env: EnvPlan{Set: env.sorted()}, Args: args, DefaultVariant: p.DefaultVariant}
	for _, v := range piVariantNames(p) {
		l.Variants = append(l.Variants, piVariant(v, p))
	}
	return l, nil
}

// piVariant turns a variant into flags: a built-in thinking level adds
// --thinking, a custom variant sets its model and thinking level.
func piVariant(name string, p config.Provider) Variant {
	custom, ok := p.Variants[name]
	if !ok {
		return Variant{Name: name, Args: []string{"--thinking", name}}
	}
	v := Variant{Name: name, Env: sortedMap(custom.Env), Shim: custom.Shim}
	if custom.Model != "" {
		v.Args = append(v.Args, "--model", custom.Model)
	}
	if custom.Reasoning != "" {
		v.Args = append(v.Args, "--thinking", custom.Reasoning)
	}
	return v
}

// piVariantNames lists the built-in thinking levels then the custom variants.
func piVariantNames(p config.Provider) []string {
	return mergeNames(config.ThinkingLevels(), p.Variants)
}

// piModelIDs are the models ak registers for a provider: its model plus any a
// variant names, in order and without repeats.
func piModelIDs(p config.Provider) []string {
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	add(p.Model)
	for _, v := range p.Variants {
		add(v.Model)
	}
	return out
}

// Shared implements SharedWriter: it returns pi's models.json with ak's
// ak-<name> providers replaced and every other entry left alone.
func (piEngine) Shared(ctx Context, cfg *config.Config) (string, []byte, error) {
	if ctx.PiHome == "" {
		return "", nil, nil
	}
	return PiModelsJSON(ctx.PiHome, cfg, ctx.Gateway)
}

// PiModelsJSON merges ak's pi providers into <agentDir>/models.json. Existing
// content is preserved: only provider ids under PiProviderPrefix are replaced.
// When the file is absent and there is nothing to add it returns "" so nothing
// is written. When ak's entries are unchanged it returns the file as it is, so
// the user's formatting is never disturbed without cause.
func PiModelsJSON(agentDir string, cfg *config.Config, gateway string) (string, []byte, error) {
	path := filepath.Join(agentDir, "models.json")
	orig, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}

	doc := map[string]any{}
	if exists && len(bytes.TrimSpace(orig)) > 0 {
		if err := json.Unmarshal(orig, &doc); err != nil {
			return "", nil, fmt.Errorf("parse %s: %w (fix it; ak will not overwrite it)", path, err)
		}
	}
	providers, _ := doc["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	// What ak owned before.
	before := map[string]string{}
	for id, v := range providers {
		if strings.HasPrefix(id, PiProviderPrefix) {
			b, _ := json.Marshal(v)
			before[id] = string(b)
		}
	}
	for id := range before {
		delete(providers, id)
	}

	// What ak wants now.
	after := map[string]string{}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.Kind != config.KindPi {
			continue
		}
		// A provider pinned to an existing pi provider needs no entry.
		if p.PiProvider != "" && !p.IsPool() {
			continue
		}
		entry := piProviderEntry(name, p, gateway)
		providers[PiProviderID(name)] = entry
		b, _ := json.Marshal(entry)
		after[PiProviderID(name)] = string(b)
	}

	if !exists && len(after) == 0 {
		return "", nil, nil
	}
	if exists && equalStringMap(before, after) {
		return path, orig, nil
	}

	doc["providers"] = providers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return path, append(out, '\n'), nil
}

// piProviderEntry is one models.json provider entry for an ak provider. A pool
// has no upstream of its own, so it points at ak's gateway.
func piProviderEntry(name string, p config.Provider, gateway string) map[string]any {
	baseURL := p.BaseURL
	if p.IsPool() {
		baseURL = strings.TrimRight(gateway, "/") + "/p/" + name
	}
	e := map[string]any{
		"baseUrl": baseURL,
		"apiKey":  "$" + EnvKey(name),
	}
	if p.Display != "" {
		e["name"] = p.Display
	} else {
		e["name"] = name
	}
	if p.PiAPI != "" {
		e["api"] = p.PiAPI
	}
	if p.PiAuthHeader {
		e["authHeader"] = true
	}
	if ids := piModelIDs(p); len(ids) > 0 {
		models := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			models = append(models, map[string]any{"id": id})
		}
		e["models"] = models
	}
	return e
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
