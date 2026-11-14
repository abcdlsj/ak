package config

import (
	"os"
	"path/filepath"
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
