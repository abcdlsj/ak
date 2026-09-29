package config

import "testing"

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(poolCfg(tc.pool, members)); err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
		})
	}
}

func TestValidatePoolCannotNest(t *testing.T) {
	cfg := Default()
	cfg.Providers["inner"] = Provider{Kind: KindClaude, Members: []string{"a"}}
	cfg.Providers["a"] = Provider{Kind: KindClaude, BaseURL: "https://a.example", APIKey: "k"}
	cfg.Providers["outer"] = Provider{Kind: KindClaude, Members: []string{"inner"}}
	if err := Validate(cfg); err == nil {
		t.Fatal("nested pool accepted")
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
