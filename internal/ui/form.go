package ui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/charmbracelet/bubbles/key"
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
	// DefaultVariant is a thinking/reasoning level or a model tier applied
	// when the provider launches without one; blank asks each time.
	DefaultVariant string
	PiProvider     string
	PiAPI          string
	PiAuth         bool
	Quota          string
	QuotaCmd       string
	// Members is the comma-separated member list of a pool; empty makes the
	// provider a normal upstream.
	Members  string
	Strategy string
	// Account is the account the provider runs on; blank for an endpoint of
	// its own.
	Account string

	// choice is what the pick step chose: an account, a vendor to sign in
	// to first, or neither for an endpoint of its own.
	choice string
	// defaults is what the account gives the provider, shown where the
	// provider leaves a field to it.
	defaults vendor.Endpoint
	existing bool
	// orig is the name being edited; Name may differ after a rename.
	orig string
	base config.Provider
}

// The pick step's choices are prefixed by what they name.
const (
	choiceAccount = "account:"
	choiceVendor  = "vendor:"
)

// NewDraft starts a draft for a new provider.
func NewDraft(name string) *Draft {
	return &Draft{Name: name, Kind: string(config.KindClaude), KeyField: "auth_token", WireAPI: "responses"}
}

// EditDraft starts a draft from an existing provider, as providers.toml
// keeps it: a field its account gives stays blank and keeps following the
// account.
func EditDraft(cfg *config.Config, name string) *Draft {
	full := cfg.Providers[name]
	p := cfg.Stored(full)
	d := &Draft{
		Name: name, Kind: string(p.Kind), Display: p.Display, BaseURL: p.BaseURL,
		Model: p.Model, Haiku: p.Haiku, Sonnet: p.Sonnet, Opus: p.Opus,
		KeyField: p.KeyField, WireAPI: p.WireAPI, Reasoning: p.Reasoning,
		DefaultVariant: p.DefaultVariant,
		PiProvider:     p.PiProvider, PiAPI: p.PiAPI, PiAuth: p.PiAuthHeader,
		Quota: p.Quota, QuotaCmd: p.QuotaCmd,
		Members: strings.Join(p.Members, ", "), Strategy: p.Strategy,
		Account:  p.Account,
		existing: true, orig: name, base: p,
	}
	d.defaults = endpointOf(cfg, p.Account, p.Kind)
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

// endpointOf is what account gives an engine of kind, if anything.
func endpointOf(cfg *config.Config, account string, kind config.Kind) vendor.Endpoint {
	v, ok := vendor.Lookup(cfg.Accounts[account].Vendor)
	if !ok {
		return vendor.Endpoint{}
	}
	e, _ := v.Endpoint(string(kind))
	return e
}

// Provider merges the draft over the provider it started from, so fields the
// form does not show (env, variants, pricing) survive an edit. It is in the
// stored form: a provider on an account leaves its endpoint to the account.
func (d *Draft) Provider() config.Provider {
	p := d.base
	p.Kind = config.Kind(d.Kind)
	p.Account = d.Account
	p.Display = strings.TrimSpace(d.Display)
	p.Model = strings.TrimSpace(d.Model)
	p.Haiku = strings.TrimSpace(d.Haiku)
	p.Sonnet = strings.TrimSpace(d.Sonnet)
	p.Opus = strings.TrimSpace(d.Opus)
	p.DefaultVariant = strings.TrimSpace(d.DefaultVariant)
	p.Reasoning = ""
	if p.Kind == config.KindCodex {
		p.Reasoning = d.Reasoning
	}
	if p.Account != "" {
		return p
	}

	p.BaseURL = strings.TrimSpace(d.BaseURL)
	p.Quota = strings.TrimSpace(d.Quota)
	p.QuotaCmd = strings.TrimSpace(d.QuotaCmd)
	p.PiProvider = strings.TrimSpace(d.PiProvider)
	p.PiAPI = strings.TrimSpace(d.PiAPI)
	p.PiAuthHeader = d.PiAuth
	// Fields of the other engine are cleared, not carried along; a default
	// value is left unset.
	p.KeyField, p.WireAPI = "", ""
	switch p.Kind {
	case config.KindClaude:
		if d.KeyField != "auth_token" {
			p.KeyField = d.KeyField
		}
	case config.KindCodex:
		if d.WireAPI != "responses" {
			p.WireAPI = d.WireAPI
		}
	}
	// A blank key keeps whatever the provider had.
	if key := strings.TrimSpace(d.Key); key != "" {
		p.SetKey(key)
	}

	// A member list turns the provider into a pool. A pool has no upstream of
	// its own, so any endpoint typed in the form is dropped.
	members := splitMembers(d.Members)
	if len(members) > 0 {
		p.Members = members
		p.Strategy = strings.TrimSpace(d.Strategy)
		p.BaseURL = ""
		if len(p.MemberModels) > 0 {
			keep := map[string]bool{}
			for _, m := range members {
				keep[m] = true
			}
			kept := map[string]string{}
			for k, v := range p.MemberModels {
				if keep[k] {
					kept[k] = v
				}
			}
			p.MemberModels = kept
		}
	} else {
		p.Members = nil
		p.Strategy = ""
		p.MemberModels = nil
	}
	return p
}

// splitMembers parses a comma-separated member list, dropping blanks.
func splitMembers(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// Orig is the name the draft started from, empty for a new provider.
func (d *Draft) Orig() string { return d.orig }

// IsNew reports whether the draft adds a provider rather than editing one.
func (d *Draft) IsNew() bool { return !d.existing }

// PickForm asks a new provider's engine and what it runs on: an account, a
// vendor to sign in to, or an endpoint of its own. It runs before Form,
// which it decides the shape of.
func (d *Draft) PickForm(cfg *config.Config) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Engine").Value(&d.Kind).
			Options(huh.NewOption("claude", string(config.KindClaude)), huh.NewOption("codex", string(config.KindCodex)), huh.NewOption("pi", string(config.KindPi))),
		huh.NewSelect[string]().Title("Runs on").Value(&d.choice).
			Description("An account, a vendor to sign in to, or your own endpoint; / filters").
			OptionsFunc(func() []huh.Option[string] { return pickOptions(cfg, config.Kind(d.Kind)) }, &d.Kind).
			Height(12),
	)).WithShowHelp(true)
}

// pickOptions lists, for an engine, the accounts that serve it, then the
// vendors to sign in to, then an endpoint of one's own.
func pickOptions(cfg *config.Config, kind config.Kind) []huh.Option[string] {
	var opts []huh.Option[string]
	for _, n := range cfg.AccountNames() {
		v, ok := vendor.Lookup(cfg.Accounts[n].Vendor)
		if _, serves := v.Endpoint(string(kind)); ok && serves {
			opts = append(opts, huh.NewOption(n+"  ·  "+v.Name, choiceAccount+n))
		}
	}
	for _, v := range vendor.All() {
		if _, serves := v.Endpoint(string(kind)); serves {
			opts = append(opts, huh.NewOption("sign in to "+v.Name+" ("+v.ID+")", choiceVendor+v.ID))
		}
	}
	return append(opts, huh.NewOption("my own endpoint", ""))
}

// SignInFirst is the vendor the pick step chose to sign in to before the
// form, if any.
func (d *Draft) SignInFirst() (vendor.Vendor, bool) {
	id, ok := strings.CutPrefix(d.choice, choiceVendor)
	if !ok {
		return vendor.Vendor{}, false
	}
	return vendor.Lookup(id)
}

// UseAccount puts the draft on an account, as the pick step or a sign-in
// chose. A blank name is the provider's own endpoint.
func (d *Draft) UseAccount(cfg *config.Config, name string) {
	d.choice, d.Account = "", name
	d.defaults = endpointOf(cfg, name, config.Kind(d.Kind))
}

// ApplyPick takes the pick step's choice of account; a vendor to sign in to
// is left for the caller (SignInFirst), who then calls UseAccount.
func (d *Draft) ApplyPick(cfg *config.Config) {
	if name, ok := strings.CutPrefix(d.choice, choiceAccount); ok {
		d.UseAccount(cfg, name)
	}
}

// formKeys moves between fields with ↑/↓ as well as tab, and changes a
// select with ←/→: every select in the form is inline.
func formKeys() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Input.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"), key.WithHelp("↓/enter", "next"))
	km.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("↑", "back"))
	km.Select.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"), key.WithHelp("↓/enter", "next"))
	km.Select.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("↑", "back"))
	km.Confirm.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"), key.WithHelp("↓/enter", "next"))
	km.Confirm.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("↑", "back"))
	return km
}

// selectOf is an inline select, changed with ←/→, so ↑/↓ stay free to move
// between fields.
func selectOf(title string, value *string, opts ...huh.Option[string]) *huh.Select[string] {
	return huh.NewSelect[string]().Title(title).Value(value).Options(opts...).Inline(true)
}

// Form builds the provider form for the draft's engine and account, known
// by now. A new provider's name may be left blank: it is then named after
// what it runs on (core.SuggestName), which the field shows.
func (d *Draft) Form(cfg *config.Config) *huh.Form {
	onAccount := d.Account != ""
	def := func(v string) string {
		if v == "" {
			return ""
		}
		return v + " (account default)"
	}

	basics := []huh.Field{
		huh.NewInput().Title("Name").Value(&d.Name).
			Description("The command becomes ak-<name>; blank takes the name shown").
			PlaceholderFunc(func() string { return core.SuggestName(cfg, d.Provider()) }, &d.BaseURL).
			Validate(func(s string) error {
				if s == "" && !d.existing {
					return nil
				}
				if err := config.ValidateName(s); err != nil {
					return err
				}
				if _, taken := cfg.Providers[s]; taken && !(d.existing && s == d.orig) {
					return fmt.Errorf("provider %q already exists", s)
				}
				return nil
			}),
		huh.NewInput().Title("Model").Value(&d.Model).Placeholder(def(d.defaults.Model)),
		huh.NewInput().Title("Display name").Value(&d.Display),
	}
	groups := []*huh.Group{huh.NewGroup(basics...)}
	if !onAccount {
		groups = append(groups, d.endpointGroup())
	}

	switch config.Kind(d.Kind) {
	case config.KindClaude:
		fields := []huh.Field{
			huh.NewInput().Title("Opus-tier model").Value(&d.Opus).Placeholder(def(d.defaults.Opus)).Description("Blank uses the primary model"),
			huh.NewInput().Title("Sonnet-tier model").Value(&d.Sonnet).Placeholder(def(d.defaults.Sonnet)),
			huh.NewInput().Title("Haiku-tier model").Value(&d.Haiku).Placeholder(def(d.defaults.Haiku)),
			selectOf("Default variant", &d.DefaultVariant, variantOptions(config.ClaudeTiers(), d.base.Variants)...),
		}
		if !onAccount {
			fields = append(fields, selectOf("Auth variable", &d.KeyField,
				huh.NewOption("ANTHROPIC_AUTH_TOKEN", "auth_token"), huh.NewOption("ANTHROPIC_API_KEY", "api_key")))
		}
		groups = append(groups, huh.NewGroup(fields...))
	case config.KindCodex:
		var fields []huh.Field
		if !onAccount {
			fields = append(fields, selectOf("Wire API", &d.WireAPI, huh.NewOption("responses", "responses"), huh.NewOption("chat", "chat")))
		}
		fields = append(fields,
			selectOf("Default reasoning effort", &d.Reasoning, reasoningOptions()...),
			selectOf("Default variant", &d.DefaultVariant, variantOptions(config.ReasoningLevels(), d.base.Variants)...),
		)
		groups = append(groups, huh.NewGroup(fields...))
	case config.KindPi:
		var fields []huh.Field
		if !onAccount {
			fields = append(fields,
				huh.NewInput().Title("Existing pi provider").Value(&d.PiProvider).
					Description("Use a provider pi already knows; blank registers ak-<name> from the endpoint"),
				selectOf("Wire API", &d.PiAPI, huh.NewOption("anthropic-messages", "anthropic-messages"), huh.NewOption("openai-completions", "openai-completions"), huh.NewOption("openai-responses", "openai-responses")),
				huh.NewConfirm().Title("Send the key as Authorization: Bearer").Value(&d.PiAuth),
			)
		}
		fields = append(fields, selectOf("Default variant", &d.DefaultVariant, variantOptions(config.ThinkingLevels(), d.base.Variants)...))
		groups = append(groups, huh.NewGroup(fields...))
	}
	return huh.NewForm(groups...).WithKeyMap(formKeys()).WithShowHelp(true)
}

// endpointGroup asks for a provider's own upstream, or the members of a pool.
func (d *Draft) endpointGroup() *huh.Group {
	keyDesc := "Or a reference: env:NAME, cmd:..., keychain:..."
	if d.existing {
		keyDesc = "Blank keeps the current key. " + keyDesc
	}
	return huh.NewGroup(
		huh.NewInput().Title("Pool members").Value(&d.Members).
			Description("Comma-separated provider names; blank for a normal provider"),
		selectOf("Pool strategy", &d.Strategy,
			huh.NewOption("order", config.StrategyOrder), huh.NewOption("rotate", config.StrategyRotate),
			huh.NewOption("least-used", config.StrategyLeastUsed), huh.NewOption("smart", config.StrategySmart)),
		huh.NewInput().Title("API endpoint").Value(&d.BaseURL).
			Validate(func(s string) error {
				if strings.TrimSpace(s) != "" || len(splitMembers(d.Members)) > 0 {
					return nil
				}
				// A pi provider may instead name one pi already knows.
				if d.Kind == string(config.KindPi) {
					return nil
				}
				return errors.New("required unless the provider is a pool")
			}),
		huh.NewInput().Title("API key").Value(&d.Key).Description(keyDesc).
			EchoMode(huh.EchoModePassword),
		huh.NewInput().Title("Balance source").Value(&d.Quota).
			Description("A quota source id, or off; blank auto-detects"),
		huh.NewInput().Title("Balance command").Value(&d.QuotaCmd).
			Description("Optional shell command printing the balance as JSON or a number"),
	)
}

func reasoningOptions() []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("(codex default)", "")}
	for _, r := range config.ReasoningLevels() {
		opts = append(opts, huh.NewOption(r, r))
	}
	return opts
}

// variantOptions lists "ask each time" plus the built-in levels and the
// provider's custom variants, in a stable order.
func variantOptions(levels []string, custom map[string]config.Variant) []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("(ask each launch)", "")}
	for _, l := range levels {
		opts = append(opts, huh.NewOption(l, l))
	}
	names := make([]string, 0, len(custom))
	for n := range custom {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		opts = append(opts, huh.NewOption(n, n))
	}
	return opts
}
