// Package vendor holds the vendors ak knows: where each one serves every
// engine it supports, and how an account signs in to it. An account names a
// vendor, and its providers take their endpoint from here, so a vendor that
// moves is followed by the next ak without touching providers.toml.
//
// The vendors are the built-in ones and the user's own, one TOML file each
// in <config dir>/vendors.d (LoadDir), which may also replace a built-in.
// The package depends on nothing else in ak.
package vendor

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

//go:embed vendors.json
var data []byte

// Vendor is one provider of models and the endpoints it serves them on.
type Vendor struct {
	ID   string `json:"id" toml:"id"`
	Name string `json:"name" toml:"name"`
	// Short is what provider names call the vendor, where its id is longer
	// than the name people use: commandcode, kimi, dpsk.
	Short      string `json:"short,omitempty" toml:"short,omitempty"`
	Category   string `json:"category" toml:"category"`
	WebsiteURL string `json:"website_url,omitempty" toml:"website_url,omitempty"`
	APIKeyURL  string `json:"api_key_url,omitempty" toml:"api_key_url,omitempty"`
	// Quota names the balance source that reads its accounts: a built-in or
	// quota.d plugin id. Empty detects one from the endpoint's host.
	Quota string `json:"quota,omitempty" toml:"quota,omitempty"`
	// Login lists the ways to sign in besides pasting a key, which every
	// vendor takes; the first that applies is offered first.
	Login []Login `json:"login,omitempty" toml:"login,omitempty"`
	// Endpoints is what the vendor serves, one entry per wire protocol. A
	// vendor that answers on Chat, Responses and Anthropic lists three, and
	// each engine takes the one it asks for.
	Endpoints []Endpoint `json:"endpoints,omitempty" toml:"endpoints,omitempty"`
	// Engines is the endpoint for each engine the vendor serves, by kind
	// (claude, codex, pi). A file may say either; Engines is filled from
	// Endpoints for the engines it can serve, and Endpoints from Engines for
	// the protocols those engines speak, so both reads give one answer.
	Engines map[string]Endpoint `json:"engines,omitempty" toml:"engines,omitempty"`
}

// Protocols a vendor endpoint speaks, as ak names them on the wire.
const (
	ProtocolAnthropic = "anthropic"
	ProtocolChat      = "chat"
	ProtocolResponses = "responses"
)

// Protocols lists the protocols ak serves, native or translated.
var Protocols = []string{ProtocolAnthropic, ProtocolChat, ProtocolResponses}

// Endpoint is how one engine talks to a vendor: the fields a provider takes
// from its account's vendor when it leaves them unset.
type Endpoint struct {
	// Protocol is the wire API this endpoint speaks: anthropic, chat or
	// responses. An [engines.<kind>] entry leaves it empty, the kind naming
	// it; a [[endpoints]] entry says it outright, which is how one vendor
	// serves several protocols.
	Protocol string `json:"protocol,omitempty" toml:"protocol,omitempty"`
	BaseURL  string `json:"base_url" toml:"base_url"`
	Model    string `json:"model,omitempty" toml:"model,omitempty"`
	// Auth is how the key is sent: bearer (Authorization: Bearer) or x-api-key.
	// Empty takes the protocol's habit, Anthropic's header for anthropic and
	// a bearer token for the two OpenAI ones.
	Auth string `json:"auth,omitempty" toml:"auth,omitempty"`

	// claude-only.
	Haiku    string `json:"haiku,omitempty" toml:"haiku,omitempty"`
	Sonnet   string `json:"sonnet,omitempty" toml:"sonnet,omitempty"`
	Opus     string `json:"opus,omitempty" toml:"opus,omitempty"`
	KeyField string `json:"key_field,omitempty" toml:"key_field,omitempty"`

	// codex-only.
	WireAPI string `json:"wire_api,omitempty" toml:"wire_api,omitempty"`

	// pi-only.
	PiAPI        string `json:"pi_api,omitempty" toml:"pi_api,omitempty"`
	PiAuthHeader bool   `json:"pi_auth_header,omitempty" toml:"pi_auth_header,omitempty"`

	// Env is the extra environment the vendor recommends, such as a context
	// window size.
	Env map[string]string `json:"env,omitempty" toml:"env,omitempty"`
}

// Login methods.
const (
	// LoginPaste takes a key the user pastes. Every vendor accepts it.
	LoginPaste = "paste"
	// LoginFile reads the key another tool already signed in with.
	LoginFile = "file"
	// LoginDevice runs a device authorization: the user approves a code in
	// the browser while ak polls for the keys.
	LoginDevice = "device"
)

// Login is one way to obtain an account's credentials.
type Login struct {
	Method string `json:"method" toml:"method"`

	// file: the first of Paths that exists ($VAR and ~ expanded, a path
	// naming an unset variable skipped), and the dotted path of the key in
	// its JSON.
	Paths []string `json:"paths,omitempty" toml:"paths,omitempty"`
	Key   string   `json:"key,omitempty" toml:"key,omitempty"`

	// device.
	Device *Device `json:"device,omitempty" toml:"device,omitempty"`
}

// Device describes a device authorization. Paths are dotted paths into the
// JSON replies; URLs and bodies take {{host}} (this computer's name) and
// {{NAME}} for any field of the start reply.
type Device struct {
	Start Request `json:"start" toml:"start"`
	// Code is what the user sees and confirms; VerifyURL the page that
	// approves it; Interval the seconds between polls; Expires when the
	// code runs out (RFC 3339).
	Code      string  `json:"code" toml:"code"`
	VerifyURL string  `json:"verify_url" toml:"verify_url"`
	Interval  string  `json:"interval,omitempty" toml:"interval,omitempty"`
	Expires   string  `json:"expires,omitempty" toml:"expires,omitempty"`
	Poll      Request `json:"poll" toml:"poll"`
	// Key is the account's API key in the approved poll reply; Secrets are
	// further credentials it holds, by the name the balance query reads them
	// as; User names the account.
	Key     string            `json:"key" toml:"key"`
	Secrets map[string]string `json:"secrets,omitempty" toml:"secrets,omitempty"`
	User    string            `json:"user,omitempty" toml:"user,omitempty"`
}

// Request is a JSON POST.
type Request struct {
	URL  string         `json:"url" toml:"url"`
	Body map[string]any `json:"body,omitempty" toml:"body,omitempty"`
}

var (
	builtins = mustParse(data)

	mu   sync.RWMutex
	all  = builtins
	errs []error
)

func mustParse(b []byte) []Vendor {
	var vs []Vendor
	if err := json.Unmarshal(b, &vs); err != nil {
		panic(fmt.Sprintf("vendor: parse vendors.json: %v", err))
	}
	for i := range vs {
		vs[i].normalize()
	}
	return vs
}

// LoadDir reads the user's vendors from dir, one *.toml each, over the
// built-ins: a file whose id is a built-in's replaces it. A file that fails
// is skipped and reported by Errors; a missing dir holds none. Each call
// starts again from the built-ins.
func LoadDir(dir string) {
	var user []Vendor
	var bad []error
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		bad = append(bad, fmt.Errorf("vendors: %w", err))
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
			continue
		}
		file := filepath.Join(dir, e.Name())
		v, err := parseFile(file)
		if err != nil {
			bad = append(bad, err)
			continue
		}
		user = append(user, v)
	}
	merged := slices.Clone(builtins)
	for _, v := range user {
		if i := slices.IndexFunc(merged, func(b Vendor) bool { return b.ID == v.ID }); i >= 0 {
			merged[i] = v
		} else {
			merged = append(merged, v)
		}
	}
	mu.Lock()
	all, errs = merged, bad
	mu.Unlock()
}

// parseFile reads one user vendor; unknown keys are refused, so a typo is
// reported rather than ignored.
func parseFile(file string) (Vendor, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return Vendor{}, err
	}
	var v Vendor
	if err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&v); err != nil {
		return Vendor{}, fmt.Errorf("%s: %w", file, err)
	}
	switch {
	case v.ID == "":
		return Vendor{}, fmt.Errorf("%s: id is required", file)
	case len(v.Engines) == 0 && len(v.Endpoints) == 0:
		return Vendor{}, fmt.Errorf("%s: no endpoint: give [[endpoints]] with a protocol, or [engines.<claude|codex|pi>]", file)
	}
	for i, e := range v.Endpoints {
		switch e.Protocol {
		case ProtocolAnthropic, ProtocolChat, ProtocolResponses:
		case "":
			return Vendor{}, fmt.Errorf("%s: endpoints[%d] has no protocol; use anthropic, chat or responses", file, i)
		default:
			return Vendor{}, fmt.Errorf("%s: endpoints[%d] names protocol %q; use anthropic, chat or responses", file, i, e.Protocol)
		}
		if e.BaseURL == "" {
			return Vendor{}, fmt.Errorf("%s: endpoints[%d] (%s) has no base_url", file, i, e.Protocol)
		}
	}
	for k, e := range v.Engines {
		if k != "claude" && k != "codex" && k != "pi" {
			return Vendor{}, fmt.Errorf("%s: unknown engine %q; use claude, codex or pi", file, k)
		}
		if e.BaseURL == "" {
			return Vendor{}, fmt.Errorf("%s: engines.%s has no base_url", file, k)
		}
	}
	v.normalize()
	v.Name = cmp.Or(v.Name, v.ID)
	return v, nil
}

// Errors lists the user vendor files the last LoadDir skipped.
func Errors() []error {
	mu.RLock()
	defer mu.RUnlock()
	return slices.Clone(errs)
}

// All returns every vendor, built-ins first in file order.
func All() []Vendor {
	mu.RLock()
	defer mu.RUnlock()
	return slices.Clone(all)
}

// Lookup finds a vendor by id.
func Lookup(id string) (Vendor, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, v := range all {
		if v.ID == id {
			return v, true
		}
	}
	return Vendor{}, false
}

// ShortName is what a provider name calls the vendor.
func (v Vendor) ShortName() string { return cmp.Or(v.Short, v.ID) }

// Endpoint returns where the vendor serves the engine of kind.
func (v Vendor) Endpoint(kind string) (Endpoint, bool) {
	e, ok := v.Engines[kind]
	return e, ok
}

// EndpointFor returns the endpoint speaking protocol, native to the vendor.
func (v Vendor) EndpointFor(protocol string) (Endpoint, bool) {
	for _, e := range v.Endpoints {
		if e.Protocol == protocol {
			return e, true
		}
	}
	return Endpoint{}, false
}

// EndpointProtocols lists the protocols the vendor serves, in ak's order.
func (v Vendor) EndpointProtocols() []string {
	var out []string
	for _, p := range Protocols {
		if _, ok := v.EndpointFor(p); ok {
			out = append(out, p)
		}
	}
	return out
}

// Kinds lists the engines the vendor serves, in ak's engine order.
func (v Vendor) Kinds() []string {
	var out []string
	for _, k := range []string{"claude", "codex", "pi"} {
		if _, ok := v.Engines[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// kindProtocol is the protocol an engine asks a vendor for when the vendor
// says nothing else: Claude Code speaks Anthropic's Messages, Codex and pi
// pick their OpenAI shape from their own settings.
func kindProtocol(kind string, e Endpoint) string {
	switch kind {
	case "claude":
		if e.Protocol != "" {
			return e.Protocol
		}
		return ProtocolAnthropic
	case "codex":
		if e.Protocol != "" {
			return e.Protocol
		}
		if e.WireAPI == "chat" {
			return ProtocolChat
		}
		return ProtocolResponses
	default: // pi
		if e.Protocol != "" {
			return e.Protocol
		}
		switch e.PiAPI {
		case "anthropic-messages":
			return ProtocolAnthropic
		case "openai-responses":
			return ProtocolResponses
		}
		return ProtocolChat
	}
}

// endpointForKind is the endpoint an engine takes, first among the vendor's
// endpoints speaking the protocol that engine prefers.
func (v Vendor) endpointForKind(kind string) (Endpoint, bool) {
	for _, proto := range enginePreference(kind) {
		if e, ok := v.EndpointFor(proto); ok {
			return e, true
		}
	}
	return Endpoint{}, false
}

// enginePreference orders the protocols an engine takes, best first: the one
// it speaks natively, then those it can be translated between.
func enginePreference(kind string) []string {
	switch kind {
	case "claude":
		return []string{ProtocolAnthropic, ProtocolChat, ProtocolResponses}
	case "codex":
		return []string{ProtocolResponses, ProtocolChat, ProtocolAnthropic}
	case "pi":
		// pi's own shape is the OpenAI one; Anthropic's Messages is what it can
		// be translated to, not what it asks for first. An endpoint that serves
		// only Anthropic is still taken, just last.
		return []string{ProtocolChat, ProtocolResponses, ProtocolAnthropic}
	}
	return []string{ProtocolAnthropic, ProtocolResponses, ProtocolChat}
}

// normalize fills Engines from Endpoints and Endpoints from Engines, so a
// file in either shape reads the same way everywhere.
func (v *Vendor) normalize() {
	if len(v.Endpoints) > 0 && len(v.Engines) == 0 {
		for _, k := range []string{"claude", "codex", "pi"} {
			if e, ok := v.endpointForKind(k); ok {
				if v.Engines == nil {
					v.Engines = map[string]Endpoint{}
				}
				v.Engines[k] = e
			}
		}
	}
	if len(v.Engines) > 0 && len(v.Endpoints) == 0 {
		for _, k := range []string{"claude", "codex", "pi"} {
			e, ok := v.Engines[k]
			if !ok {
				continue
			}
			e.Protocol = kindProtocol(k, e)
			v.Endpoints = append(v.Endpoints, e)
		}
	}
	// A file may give both; the endpoints it gives keep their own order, and
	// an engine table fills in a protocol they did not name.
	for _, k := range []string{"claude", "codex", "pi"} {
		e, ok := v.Engines[k]
		if !ok {
			continue
		}
		if e.Protocol == "" {
			e.Protocol = kindProtocol(k, e)
			v.Engines[k] = e
		}
		if _, ok := v.EndpointFor(e.Protocol); !ok {
			v.Endpoints = append(v.Endpoints, e)
		}
	}
}

// authStyle is how an endpoint wants the key sent.
func (e Endpoint) AuthStyle() string {
	if e.Auth != "" {
		return e.Auth
	}
	if e.Protocol == ProtocolAnthropic {
		return "x-api-key"
	}
	return "bearer"
}

// Logins lists the ways to sign in, the vendor's own first, pasting a key
// last.
func (v Vendor) Logins() []Login {
	return append(slices.Clone(v.Login), Login{Method: LoginPaste})
}
