// Package preset holds built-in vendor presets: the endpoint, model and
// protocol of a known provider, so adding one needs only a name and a key.
// The data is taken from cc-switch's preset lists.
package preset

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/abcdlsj/ak/internal/config"
)

//go:embed presets.json
var data []byte

// Preset is one vendor on one engine. The same vendor shares its ID across
// engines; (ID, Kind) is unique.
type Preset struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Kind     config.Kind `json:"kind"`
	Category string      `json:"category"`
	BaseURL  string      `json:"base_url"`
	Model    string      `json:"model,omitempty"`

	// claude-only.
	Haiku    string `json:"haiku,omitempty"`
	Sonnet   string `json:"sonnet,omitempty"`
	Opus     string `json:"opus,omitempty"`
	KeyField string `json:"key_field,omitempty"`

	// codex-only.
	WireAPI string `json:"wire_api,omitempty"`

	// pi-only.
	PiAPI        string `json:"pi_api,omitempty"`
	PiAuthHeader bool   `json:"pi_auth_header,omitempty"`

	// Env is the extra environment the vendor recommends, such as a context
	// window size.
	Env map[string]string `json:"env,omitempty"`

	WebsiteURL string `json:"website_url,omitempty"`
	APIKeyURL  string `json:"api_key_url,omitempty"`
}

var all = mustParse(data)

func mustParse(b []byte) []Preset {
	var ps []Preset
	if err := json.Unmarshal(b, &ps); err != nil {
		panic(fmt.Sprintf("preset: parse presets.json: %v", err))
	}
	return ps
}

// All returns every preset, in file order.
func All() []Preset { return append([]Preset(nil), all...) }

// ForKind returns the presets for one engine, in file order.
func ForKind(kind config.Kind) []Preset {
	var out []Preset
	for _, p := range all {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// Kinds lists the engines a preset ID has, in file order.
func Kinds(id string) []config.Kind {
	var out []config.Kind
	for _, p := range all {
		if p.ID == id {
			out = append(out, p.Kind)
		}
	}
	return out
}

// Lookup finds the preset with this ID for the engine. An empty kind matches
// when the ID has only one engine.
func Lookup(id string, kind config.Kind) (Preset, bool) {
	if kind == "" {
		kinds := Kinds(id)
		if len(kinds) != 1 {
			return Preset{}, false
		}
		kind = kinds[0]
	}
	for _, p := range all {
		if p.ID == id && p.Kind == kind {
			return p, true
		}
	}
	return Preset{}, false
}

// Apply fills a provider from the preset. It sets the kind and every field
// the preset names, keeps a display name already set and leaves the key
// alone; env entries already on the provider win.
func (ps Preset) Apply(p *config.Provider) {
	p.Kind = ps.Kind
	p.BaseURL = ps.BaseURL
	p.Model = ps.Model
	if p.Display == "" {
		p.Display = ps.Name
	}
	p.Haiku, p.Sonnet, p.Opus, p.KeyField = "", "", "", ""
	p.WireAPI, p.PiAPI, p.PiAuthHeader = "", "", false
	switch ps.Kind {
	case config.KindClaude:
		p.Haiku, p.Sonnet, p.Opus, p.KeyField = ps.Haiku, ps.Sonnet, ps.Opus, ps.KeyField
	case config.KindCodex:
		p.WireAPI = ps.WireAPI
	case config.KindPi:
		p.PiAPI, p.PiAuthHeader = ps.PiAPI, ps.PiAuthHeader
	}
	if len(ps.Env) > 0 {
		env := maps.Clone(ps.Env)
		maps.Copy(env, p.Env)
		p.Env = env
	}
}
