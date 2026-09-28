package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/charmbracelet/huh"
)

// Draft is the editable state of a provider, shared by `ak add`, `ak edit`
// and the TUI.
type Draft struct {
	Name      string
	Kind      string
	Display   string
	BaseURL   string
	Key       string // blank keeps the current key when editing
	Model     string
	Haiku     string
	Sonnet    string
	Opus      string
	KeyField  string
	WireAPI   string
	Reasoning string

	existing bool
	// orig is the name being edited; Name may differ after a rename.
	orig string
	base config.Provider
}

// NewDraft starts a draft for a new provider.
func NewDraft(name string) *Draft {
	return &Draft{Name: name, Kind: string(config.KindClaude), KeyField: "auth_token", WireAPI: "responses"}
}

// EditDraft starts a draft from an existing provider.
func EditDraft(name string, p config.Provider) *Draft {
	d := &Draft{
		Name: name, Kind: string(p.Kind), Display: p.Display, BaseURL: p.BaseURL,
		Model: p.Model, Haiku: p.Haiku, Sonnet: p.Sonnet, Opus: p.Opus,
		KeyField: p.KeyField, WireAPI: p.WireAPI, Reasoning: p.Reasoning,
		existing: true, orig: name, base: p,
	}
	if d.KeyField == "" {
		d.KeyField = "auth_token"
	}
	if d.WireAPI == "" {
		d.WireAPI = "responses"
	}
	if p.APIKeyRef != "" {
		d.Key = p.APIKeyRef
	}
	return d
}

// Provider merges the draft over the provider it started from, so fields the
// form does not show (env, variants, pricing) survive an edit.
func (d *Draft) Provider() config.Provider {
	p := d.base
	p.Kind = config.Kind(d.Kind)
	p.Display = strings.TrimSpace(d.Display)
	p.BaseURL = strings.TrimSpace(d.BaseURL)
	p.Model = strings.TrimSpace(d.Model)
	p.Haiku = strings.TrimSpace(d.Haiku)
	p.Sonnet = strings.TrimSpace(d.Sonnet)
	p.Opus = strings.TrimSpace(d.Opus)
	// Fields of the other engine are cleared, not carried along; a default
	// value is left unset.
	p.KeyField, p.WireAPI, p.Reasoning = "", "", ""
	switch p.Kind {
	case config.KindClaude:
		if d.KeyField != "auth_token" {
			p.KeyField = d.KeyField
		}
	case config.KindCodex:
		if d.WireAPI != "responses" {
			p.WireAPI = d.WireAPI
		}
		p.Reasoning = d.Reasoning
	}
	// A blank key keeps whatever the provider had.
	if key := strings.TrimSpace(d.Key); key != "" {
		p.SetKey(key)
	}
	return p
}

// Orig is the name the draft started from, empty for a new provider.
func (d *Draft) Orig() string { return d.orig }

// Form builds the provider form. taken reports names already in use.
func (d *Draft) Form(taken func(string) bool) *huh.Form {
	isKind := func(k config.Kind) func() bool {
		return func() bool { return d.Kind != string(k) }
	}

	keyDesc := "Or a reference: env:NAME, cmd:..., keychain:..."
	if d.existing {
		keyDesc = "Blank keeps the current key. " + keyDesc
	}

	basics := []huh.Field{
		huh.NewInput().Title("Name").Value(&d.Name).
			Description("The command becomes ak-<name>").
			Validate(func(s string) error {
				if err := config.ValidateName(s); err != nil {
					return err
				}
				if taken(s) && !(d.existing && s == d.orig) {
					return fmt.Errorf("provider %q already exists", s)
				}
				return nil
			}),
	}
	if !d.existing {
		basics = append(basics,
			huh.NewSelect[string]().Title("Engine").Value(&d.Kind).
				Options(huh.NewOption("claude", string(config.KindClaude)), huh.NewOption("codex", string(config.KindCodex))),
		)
	}
	basics = append(basics,
		huh.NewInput().Title("API endpoint").Value(&d.BaseURL).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("required")
				}
				return nil
			}),
		huh.NewInput().Title("API key").Value(&d.Key).Description(keyDesc).
			EchoMode(huh.EchoModePassword),
		huh.NewInput().Title("Model").Value(&d.Model),
		huh.NewInput().Title("Display name").Value(&d.Display),
	)

	claude := huh.NewGroup(
		huh.NewInput().Title("Opus-tier model").Value(&d.Opus).Description("Blank uses the primary model"),
		huh.NewInput().Title("Sonnet-tier model").Value(&d.Sonnet),
		huh.NewInput().Title("Haiku-tier model").Value(&d.Haiku),
		huh.NewSelect[string]().Title("Auth variable").Value(&d.KeyField).
			Options(huh.NewOption("ANTHROPIC_AUTH_TOKEN", "auth_token"), huh.NewOption("ANTHROPIC_API_KEY", "api_key")),
	).WithHideFunc(isKind(config.KindClaude))

	codex := huh.NewGroup(
		huh.NewSelect[string]().Title("Wire API").Value(&d.WireAPI).
			Options(huh.NewOption("responses", "responses"), huh.NewOption("chat", "chat")),
		huh.NewSelect[string]().Title("Default reasoning effort").Value(&d.Reasoning).
			Options(reasoningOptions()...),
	).WithHideFunc(isKind(config.KindCodex))

	return huh.NewForm(huh.NewGroup(basics...), claude, codex).WithShowHelp(true)
}

func reasoningOptions() []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("(codex default)", "")}
	for _, r := range config.ReasoningLevels() {
		opts = append(opts, huh.NewOption(r, r))
	}
	return opts
}
