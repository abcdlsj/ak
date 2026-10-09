package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func poolCfg(pool Provider, members map[string]Provider) *Config {
	cfg := Default()
	for name, m := range members {
		cfg.Providers[name] = m
	}
	cfg.Providers["pool"] = pool
	return cfg
}

func TestValidatePoolOK(t *testing.T) {
	cfg := poolCfg(
		Provider{Kind: KindClaude, Members: []string{"a", "b"}, Strategy: StrategyRotate, MemberModels: map[string]string{"b": "other"}},
		map[string]Provider{
			"a": {Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k"},
			"b": {Kind: KindClaude, BaseURL: "https://b.example", APIKey: "k"},
		},
	)
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid pool rejected: %v", err)
	}
}

func TestValidatePoolErrors(t *testing.T) {
	members := map[string]Provider{
		"a": {Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k"},
		"c": {Kind: KindCodex, BaseURL: "https://c.example", APIKey: "k"},
	}
	cases := []struct {
		name string
		pool Provider
	}{
		{"unknown member", Provider{Kind: KindClaude, Members: []string{"missing"}}},
		{"self member", Provider{Kind: KindClaude, Members: []string{"pool"}}},
		{"kind mismatch", Provider{Kind: KindClaude, Members: []string{"c"}}},
		{"duplicate member", Provider{Kind: KindClaude, Members: []string{"a", "a"}}},
		{"bad strategy", Provider{Kind: KindClaude, Members: []string{"a"}, Strategy: "random"}},
		{"mapping for non-member", Provider{Kind: KindClaude, Members: []string{"a"}, MemberModels: map[string]string{"z": "m"}}},
		{"wire mismatch", Provider{Kind: KindCodex, Members: []string{"c"}, WireAPI: "chat"}},
		{"bad pool wire_api", Provider{Kind: KindCodex, Members: []string{"c"}, WireAPI: "grpc"}},
		{"bad env name", Provider{Kind: KindClaude, Members: []string{"a"}, Env: map[string]string{"A B": "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(poolCfg(tc.pool, members)); err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
		})
	}
}

func TestValidatePoolNesting(t *testing.T) {
	base := func() *Config {
		cfg := Default()
		cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k"}
		cfg.Providers["b"] = Provider{Kind: KindClaude, BaseURL: "https://b.example", APIKey: "k"}
		cfg.Providers["inner"] = Provider{Kind: KindClaude, Members: []string{"a", "b"}}
		cfg.Providers["outer"] = Provider{Kind: KindClaude, Members: []string{"inner", "a"}, Strategy: StrategySmart}
		return cfg
	}
	cfg := base()
	if err := Validate(cfg); err != nil {
		t.Fatalf("nested pool rejected: %v", err)
	}
	if got := cfg.Leaves("outer"); strings.Join(got, ",") != "a,b" {
		t.Errorf("Leaves(outer) = %v, want [a b]", got)
	}
	if got := cfg.Leaves("a"); strings.Join(got, ",") != "a" {
		t.Errorf("Leaves(a) = %v", got)
	}

	cyc := base()
	cyc.Providers["inner"] = Provider{Kind: KindClaude, Members: []string{"a", "outer"}}
	if err := Validate(cyc); err == nil || !strings.Contains(err.Error(), "leads back") {
		t.Fatalf("cycle: %v", err)
	}

	deep := base()
	prev := "a"
	for i := 0; i < MaxPoolDepth+1; i++ {
		n := fmt.Sprintf("p%d", i)
		deep.Providers[n] = Provider{Kind: KindClaude, Members: []string{prev}}
		prev = n
	}
	if err := Validate(deep); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Fatalf("depth: %v", err)
	}

	kind := base()
	kind.Providers["inner"] = Provider{Kind: KindCodex, Members: []string{"c"}}
	kind.Providers["c"] = Provider{Kind: KindCodex, BaseURL: "https://c.example", APIKey: "k"}
	if err := Validate(kind); err == nil {
		t.Fatal("a claude pool took a codex pool")
	}
}

func TestDefaultGatewayAddr(t *testing.T) {
	if got := Default().Settings.GatewayURL(); got != "http://"+DefaultGatewayAddr {
		t.Errorf("GatewayURL = %q", got)
	}
}

func TestValidateQuota(t *testing.T) {
	cfg := Default()
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", Quota: "deepseek"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("a known quota id was rejected: %v", err)
	}
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", Quota: "off"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("off was rejected: %v", err)
	}
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", Quota: "not a source"}
	if err := Validate(cfg); err == nil {
		t.Fatal("a malformed quota id was accepted")
	}
}

func TestValidateConcurrencyBounds(t *testing.T) {
	cfg := Default()
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", MaxConcurrency: -1}
	if err := Validate(cfg); err == nil {
		t.Fatal("a negative max_concurrency was accepted")
	}
	cfg = Default()
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", MaxConcurrency: 4}
	if err := Validate(cfg); err != nil {
		t.Fatalf("a positive max_concurrency was rejected: %v", err)
	}
	cfg.Settings.MaxInflight = -1
	if err := Validate(cfg); err == nil {
		t.Fatal("a negative max_inflight was accepted")
	}
}

func TestDefaultMaxInflightRoundTrips(t *testing.T) {
	cfg := Default()
	if cfg.Settings.MaxInflight != DefaultMaxInflight {
		t.Fatalf("Default MaxInflight = %d, want %d", cfg.Settings.MaxInflight, DefaultMaxInflight)
	}
	// An explicit 0 (no bound) must survive a save/load cycle.
	cfg.Settings.MaxInflight = 0
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	back := Default()
	if err := toml.Unmarshal(data, back); err != nil {
		t.Fatal(err)
	}
	if back.Settings.MaxInflight != 0 {
		t.Errorf("max_inflight = %d after a round trip, want 0", back.Settings.MaxInflight)
	}
}

func TestValidateEnvNames(t *testing.T) {
	for _, k := range []string{"A B", "X;rm -rf", "$(id)", "1X", ""} {
		cfg := Default()
		cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", Env: map[string]string{k: "v"}}
		if err := Validate(cfg); err == nil {
			t.Errorf("env name %q accepted", k)
		}
		cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k",
			Variants: map[string]Variant{"v": {Env: map[string]string{k: "v"}}}}
		if err := Validate(cfg); err == nil {
			t.Errorf("variant env name %q accepted", k)
		}
	}
	cfg := Default()
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k", Env: map[string]string{"HTTPS_PROXY": "v", "_x1": "v"}}
	if err := Validate(cfg); err != nil {
		t.Errorf("valid env names rejected: %v", err)
	}
}
