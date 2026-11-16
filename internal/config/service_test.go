package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// serviceCfg has the accounts of the user's setup: bili (claude and codex),
// command-code and opendesign.
func serviceCfg(t *testing.T) *Config {
	t.Helper()
	cfg := Default()
	cfg.Accounts["command-code"] = Account{Vendor: "command-code", APIKey: "k1"}
	cfg.Accounts["opendesign"] = Account{Vendor: "opendesign", APIKey: "k2"}
	cfg.Accounts["kimi"] = Account{Vendor: "kimi-coding", APIKey: "k3"}
	return cfg
}

var hash = "sha256:" + strings.Repeat("ab", 32)

func TestParseModel(t *testing.T) {
	cfg := serviceCfg(t)
	cfg.Service.Routes = map[string]Route{"fast": {Model: "command-code/deepseek/deepseek-v4.1-flash"}}
	cases := []struct {
		name string
		want ModelRef
		err  bool
	}{
		{"command-code", ModelRef{Account: "command-code"}, false},
		{"command-code/deepseek/deepseek-v4.1-flash", ModelRef{Account: "command-code", Model: "deepseek/deepseek-v4.1-flash"}, false},
		{"fast", ModelRef{Route: "fast"}, false},
		{"nobody/x", ModelRef{}, true},
		{"command-code/", ModelRef{}, true},
		{"claude-sonnet-5-5", ModelRef{}, true},
	}
	for _, c := range cases {
		got, err := cfg.ParseModel(c.name)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ParseModel(%q) = %+v, %v; want %+v, error %v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestOnAccount(t *testing.T) {
	cfg := serviceCfg(t)
	p, ok := cfg.OnAccount("command-code", KindCodex)
	if !ok || p.BaseURL != "https://api.commandcode.ai/provider/v1" || p.APIKey != "k1" || p.WireAPI != "responses" {
		t.Errorf("codex on command-code = %+v, %v", p, ok)
	}
	if _, ok := cfg.OnAccount("opencode", KindCodex); ok {
		t.Error("an account that does not exist gave a provider")
	}
}

func TestValidateServiceOK(t *testing.T) {
	cfg := serviceCfg(t)
	cfg.Service = Service{
		Addr:      "127.0.0.1:8787",
		PublicURL: "https://ak.example.com",
		Routes: map[string]Route{
			"main": {Members: []string{"opendesign/deepseek-v4-pro", "command-code", "fast"}, Strategy: StrategySmart},
			"fast": {Model: "command-code/deepseek/deepseek-v4.1-flash"},
			"all":  {Members: []string{"main", "kimi"}},
		},
		Tokens: map[string]Token{"laptop": {Hash: hash, Routes: []string{"main", "kimi"}, Created: time.Now()}},
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateServiceErrors(t *testing.T) {
	cases := map[string]Service{
		"bad addr":         {Addr: "8787"},
		"bad public url":   {PublicURL: "ak.example.com"},
		"public url query": {PublicURL: "https://ak.example.com/?x=1"},
		"route an account": {Routes: map[string]Route{"kimi": {Model: "command-code"}}},
		"neither":          {Routes: map[string]Route{"x": {}}},
		"both":             {Routes: map[string]Route{"x": {Model: "kimi", Members: []string{"kimi"}}}},
		"alias strategy":   {Routes: map[string]Route{"x": {Model: "kimi", Strategy: "smart"}}},
		"bad strategy":     {Routes: map[string]Route{"x": {Members: []string{"kimi"}, Strategy: "random"}}},
		"unknown member":   {Routes: map[string]Route{"x": {Members: []string{"nobody/m"}}}},
		"duplicate member": {Routes: map[string]Route{"x": {Members: []string{"kimi", "kimi"}}}},
		"cycle":            {Routes: map[string]Route{"x": {Members: []string{"y"}}, "y": {Model: "x"}}},
		"self":             {Routes: map[string]Route{"x": {Members: []string{"x"}}}},
		"bad route name":   {Routes: map[string]Route{"A/b": {Model: "kimi"}}},
		"token no hash":    {Tokens: map[string]Token{"laptop": {Hash: "plain"}}},
		"token bad route":  {Tokens: map[string]Token{"laptop": {Hash: hash, Routes: []string{"nope"}}}},
		"bad token name":   {Tokens: map[string]Token{"Laptop!": {Hash: hash}}},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := serviceCfg(t)
			cfg.Service = s
			if err := Validate(cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestServiceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)
	t.Setenv("HOME", t.TempDir())
	cfg := serviceCfg(t)
	cfg.Service = Service{
		Addr:      "127.0.0.1:8787",
		PublicURL: "https://ak.example.com",
		Routes:    map[string]Route{"main": {Members: []string{"kimi", "command-code"}, Strategy: StrategyOrder}},
		Tokens:    map[string]Token{"laptop": {Hash: hash, Created: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}},
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Service.Addr != cfg.Service.Addr || got.Service.PublicURL != cfg.Service.PublicURL || len(got.Service.Routes["main"].Members) != 2 ||
		got.Service.Tokens["laptop"].Hash != hash || !got.Service.Tokens["laptop"].Created.Equal(cfg.Service.Tokens["laptop"].Created) {
		t.Errorf("service did not round-trip: %+v", got.Service)
	}
	// A config with no service writes no [service] table.
	if err := Save(serviceCfg(t)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "providers.toml"))
	if strings.Contains(string(b), "[service") {
		t.Errorf("an empty service was written:\n%s", b)
	}
}

// A route naming a provider pool takes the provider's members on load, and
// writes only the provider back: the members are the provider's to keep.
func TestRouteOverAProvider(t *testing.T) {
	cfg := serviceCfg(t)
	cfg.Providers["a-claude"] = Provider{Kind: KindClaude, Account: "command-code", Model: "m-a"}
	cfg.Providers["b-claude"] = Provider{Kind: KindClaude, Account: "opendesign", Model: "m-b"}
	cfg.Providers["mypool"] = Provider{Kind: KindClaude, Members: []string{"a-claude", "b-claude"},
		MemberModels: map[string]string{"b-claude": "m-mapped"}}
	cfg.Service.Routes = map[string]Route{"dpsk": {Provider: "mypool"}}
	cfg.Resolve()

	got := cfg.Service.Routes["dpsk"]
	if strings.Join(got.Members, ",") != "command-code/m-a,opendesign/m-mapped" {
		t.Fatalf("members = %v, want the pool's accounts and models", got.Members)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid route over a provider: %v", err)
	}
	// Writing it back keeps `provider = "mypool"` alone.
	stored := *cfg
	stored.Service.Routes = maps.Clone(cfg.Service.Routes)
	stored.collapseRoutes()
	if r := stored.Service.Routes["dpsk"]; len(r.Members) != 0 || r.Provider != "mypool" {
		t.Errorf("stored route = %+v, want only the provider", r)
	}
}

// A route may not name something that is no pool, nor a pool whose members
// are different engines.
func TestRouteOverAProviderThatIsNoPool(t *testing.T) {
	cfg := serviceCfg(t)
	cfg.Providers["a-claude"] = Provider{Kind: KindClaude, Account: "command-code", Model: "m-a"}
	cfg.Providers["a-codex"] = Provider{Kind: KindCodex, Account: "command-code"}
	cfg.Providers["mixed"] = Provider{Kind: KindClaude, Members: []string{"a-claude"}}
	cfg.Service.Routes = map[string]Route{"one": {Provider: "a-claude"}}
	cfg.Resolve()
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "no pool") {
		t.Errorf("route over a plain provider: %v", err)
	}

	cfg.Providers["mixed"] = Provider{Kind: KindClaude, Members: []string{"a-claude"}}
	cfg.Providers["codexy"] = Provider{Kind: KindClaude, Members: []string{"a-codex"}}
	cfg.Service.Routes = map[string]Route{"two": {Provider: "codexy"}}
	cfg.Resolve()
	if err := Validate(cfg); err == nil {
		t.Error("a route over another engine's pool was accepted")
	}
}

// A pool member may name an account and the model to ask it for, and ak makes
// the provider that stands for it: never written, never listed, always
// resolvable by everything that reads a pool's members.
func TestPoolMemberNamesAnAccountModel(t *testing.T) {
	cfg := serviceCfg(t)
	cfg.Providers["pool"] = Provider{Kind: KindClaude,
		Members: []string{"command-code/mine", "opendesign", "kimi"}}
	cfg.Resolve()

	ref, ok := cfg.MemberRef("command-code/mine")
	if !ok || ref.Account != "command-code" || ref.Model != "mine" {
		t.Fatalf("MemberRef = %+v, %v", ref, ok)
	}
	if _, ok := cfg.MemberRef("nobody/mine"); ok {
		t.Error("an unknown account was accepted")
	}
	if p, ok := cfg.Providers["command-code/mine"]; !ok || p.Account != "command-code" || p.Model != "mine" {
		t.Errorf("member provider = %+v, %v", p, ok)
	}
	// An account member keeps the model its plan serves.
	if p := cfg.Providers["opendesign"]; p.Account != "opendesign" {
		t.Errorf("account member = %+v", p)
	}
	if got := cfg.Names(); slices.Contains(got, "command-code/mine") {
		t.Errorf("an implicit member was listed: %v", got)
	}
	if got := cfg.Leaves("pool"); len(got) != 3 {
		t.Errorf("leaves = %v, want every member", got)
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("valid: %v", err)
	}
}
