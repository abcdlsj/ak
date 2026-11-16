package vendor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A user vendor is added from vendors.d, one with a built-in's id replaces
// it, and a broken file is reported and skipped.
func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("bili.toml", `id = "bili"
name = "bilibili AI Coding"
quota = "bili"
[engines.claude]
base_url = "http://bili.example/api"
[engines.codex]
base_url = "http://bili.example/api/v1/codex"
model = "gpt"
`)
	write("kimi.toml", "id = \"kimi\"\n[engines.pi]\nbase_url = \"https://mine\"\n")
	write("bad.toml", "id = \"bad\"\ntypo = 1\n")
	LoadDir(dir)
	t.Cleanup(func() { LoadDir("") })

	v, ok := Lookup("bili")
	if !ok || v.Quota != "bili" || len(v.Kinds()) != 2 || v.Engines["codex"].Model != "gpt" {
		t.Fatalf("bili = %+v, %v", v, ok)
	}
	if k, _ := Lookup("kimi"); len(k.Kinds()) != 1 || k.Engines["pi"].BaseURL != "https://mine" {
		t.Errorf("kimi was not replaced: %+v", k)
	}
	if errs := Errors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "bad.toml") {
		t.Errorf("errors = %v", errs)
	}
}

// A vendor may say what it serves by protocol instead of by engine, and one
// that serves two protocols serves both engines that ask for them.
func TestEndpointsByProtocol(t *testing.T) {
	dir := t.TempDir()
	body := `id = "relay"
[[endpoints]]
protocol = "anthropic"
base_url = "https://relay.example"
model = "claude-sonnet-5-5"
[[endpoints]]
protocol = "responses"
base_url = "https://relay.example/v1"
model = "gpt-6-luna"
auth = "bearer"
`
	if err := os.WriteFile(filepath.Join(dir, "relay.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadDir(dir)
	t.Cleanup(func() { LoadDir("") })

	v, ok := Lookup("relay")
	if !ok {
		t.Fatal("relay was not loaded")
	}
	if got := v.EndpointProtocols(); len(got) != 2 || got[0] != ProtocolAnthropic || got[1] != ProtocolResponses {
		t.Errorf("protocols = %v", got)
	}
	// Both engines take the protocol they speak, from the one list.
	if e, ok := v.Endpoint("claude"); !ok || e.Model != "claude-sonnet-5-5" {
		t.Errorf("claude = %+v, %v", e, ok)
	}
	if e, ok := v.Endpoint("codex"); !ok || e.Model != "gpt-6-luna" || e.AuthStyle() != "bearer" {
		t.Errorf("codex = %+v, %v", e, ok)
	}
	// A vendor with no Chat endpoint serves no pi engine on Chat.
	if e, ok := v.EndpointFor(ProtocolChat); ok {
		t.Errorf("chat = %+v, want none", e)
	}
	if errs := Errors(); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
}

// An endpoint entry that names no protocol, or an unknown one, is a file ak
// cannot read: the vendor is skipped rather than guessed at.
func TestEndpointsNeedAKnownProtocol(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.toml"), []byte("id = \"x\"\n[[endpoints]]\nbase_url = \"https://x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadDir(dir)
	t.Cleanup(func() { LoadDir("") })
	if errs := Errors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "no protocol") {
		t.Errorf("errors = %v, want one naming the missing protocol", errs)
	}
}

// Each engine asks for what it speaks natively: Claude Code Anthropic's
// Messages, Codex Responses, pi the OpenAI chat API. A vendor serving all
// three hands pi its chat endpoint, so a request through it needs no
// translation — and one serving only Anthropic still gets taken, last.
func TestEnginePreference(t *testing.T) {
	three := Vendor{Endpoints: []Endpoint{
		{Protocol: ProtocolAnthropic, BaseURL: "https://relay.example"},
		{Protocol: ProtocolResponses, BaseURL: "https://relay.example/v1"},
		{Protocol: ProtocolChat, BaseURL: "https://relay.example/v1"},
	}}
	for kind, want := range map[string]string{
		"claude": ProtocolAnthropic,
		"codex":  ProtocolResponses,
		"pi":     ProtocolChat,
	} {
		e, ok := three.endpointForKind(kind)
		if !ok || e.Protocol != want {
			t.Errorf("%s takes %q, want %q", kind, e.Protocol, want)
		}
	}

	anthOnly := Vendor{Endpoints: []Endpoint{{Protocol: ProtocolAnthropic, BaseURL: "https://a.example"}}}
	if e, ok := anthOnly.endpointForKind("pi"); !ok || e.Protocol != ProtocolAnthropic {
		t.Errorf("pi on an Anthropic-only vendor takes %q, %v", e.Protocol, ok)
	}
}
