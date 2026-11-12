// Package vendor holds the vendors ak knows: where each one serves every
// engine it supports, and how an account signs in to it. An account names a
// vendor, and its providers take their endpoint from here, so a vendor that
// moves is followed by the next ak without touching providers.toml.
//
// The package is pure data and depends on nothing else in ak.
package vendor

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

//go:embed vendors.json
var data []byte

// Vendor is one provider of models and the endpoints it serves them on.
type Vendor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Short is what provider names call the vendor, where its id is longer
	// than the name people use: commandcode, kimi, dpsk.
	Short      string `json:"short,omitempty"`
	Category   string `json:"category"`
	WebsiteURL string `json:"website_url,omitempty"`
	APIKeyURL  string `json:"api_key_url,omitempty"`
	// Login lists the ways to sign in besides pasting a key, which every
	// vendor takes; the first that applies is offered first.
	Login []Login `json:"login,omitempty"`
	// Engines is the endpoint for each engine the vendor serves, by kind
	// (claude, codex, pi).
	Engines map[string]Endpoint `json:"engines"`
}

// Endpoint is how one engine talks to a vendor: the fields a provider takes
// from its account's vendor when it leaves them unset.
type Endpoint struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model,omitempty"`

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
	Method string `json:"method"`

	// file: the first of Paths that exists ($VAR and ~ expanded, a path
	// naming an unset variable skipped), and the dotted path of the key in
	// its JSON.
	Paths []string `json:"paths,omitempty"`
	Key   string   `json:"key,omitempty"`

	// device.
	Device *Device `json:"device,omitempty"`
}

// Device describes a device authorization. Paths are dotted paths into the
// JSON replies; URLs and bodies take {{host}} (this computer's name) and
// {{NAME}} for any field of the start reply.
type Device struct {
	Start Request `json:"start"`
	// Code is what the user sees and confirms; VerifyURL the page that
	// approves it; Interval the seconds between polls; Expires when the
	// code runs out (RFC 3339).
	Code      string  `json:"code"`
	VerifyURL string  `json:"verify_url"`
	Interval  string  `json:"interval,omitempty"`
	Expires   string  `json:"expires,omitempty"`
	Poll      Request `json:"poll"`
	// Key is the account's API key in the approved poll reply; Secrets are
	// further credentials it holds, by the name the balance query reads them
	// as; User names the account.
	Key     string            `json:"key"`
	Secrets map[string]string `json:"secrets,omitempty"`
	User    string            `json:"user,omitempty"`
}

// Request is a JSON POST.
type Request struct {
	URL  string         `json:"url"`
	Body map[string]any `json:"body,omitempty"`
}

var all = mustParse(data)

func mustParse(b []byte) []Vendor {
	var vs []Vendor
	if err := json.Unmarshal(b, &vs); err != nil {
		panic(fmt.Sprintf("vendor: parse vendors.json: %v", err))
	}
	return vs
}

// All returns every vendor, in file order.
func All() []Vendor { return slices.Clone(all) }

// Lookup finds a vendor by id.
func Lookup(id string) (Vendor, bool) {
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

// Logins lists the ways to sign in, the vendor's own first, pasting a key
// last.
func (v Vendor) Logins() []Login {
	return append(slices.Clone(v.Login), Login{Method: LoginPaste})
}
