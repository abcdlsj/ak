package config

import "testing"

func piCfg(p Provider) *Config {
	cfg := Default()
	cfg.Providers["x"] = p
	return cfg
}

func TestValidatePiPool(t *testing.T) {
	cfg := Default()
	cfg.Providers["a"] = Provider{Kind: KindPi, BaseURL: "https://a", Model: "m"}
	cfg.Providers["b"] = Provider{Kind: KindPi, BaseURL: "https://b", Model: "m"}
	cfg.Providers["pool"] = Provider{Kind: KindPi, Members: []string{"a", "b"}, Model: "logical"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("a valid pi pool was rejected: %v", err)
	}

	// A pi pool with no model has nothing for pi to select.
	cfg.Providers["pool"] = Provider{Kind: KindPi, Members: []string{"a", "b"}}
	if err := Validate(cfg); err == nil {
		t.Fatal("a pi pool without a model was accepted")
	}

	// A member that uses an existing pi provider has no endpoint to forward to.
	cfg.Providers["pin"] = Provider{Kind: KindPi, PiProvider: "x", Model: "m"}
	cfg.Providers["pool"] = Provider{Kind: KindPi, Members: []string{"pin"}, Model: "logical"}
	if err := Validate(cfg); err == nil {
		t.Fatal("a pi pool with an endpoint-less member was accepted")
	}
}

func TestValidatePi(t *testing.T) {
	ok := []Provider{
		{Kind: KindPi, BaseURL: "https://relay.example", Model: "m1"},
		{Kind: KindPi, BaseURL: "https://relay.example", Model: "m1", PiAPI: "openai-completions"},
		{Kind: KindPi, PiProvider: "commandcode", Model: "deepseek/x"},
		{Kind: KindPi, BaseURL: "https://relay.example", Variants: map[string]Variant{"fast": {Model: "m2"}}},
	}
	for i, p := range ok {
		if err := Validate(piCfg(p)); err != nil {
			t.Errorf("case %d rejected: %v", i, err)
		}
	}

	bad := []Provider{
		{Kind: KindPi},                       // no endpoint, no pi_provider
		{Kind: KindPi, BaseURL: "https://x"}, // no model
		{Kind: KindPi, BaseURL: "https://x", Model: "m", PiAPI: "grpc"},                                       // bad api
		{Kind: KindPi, BaseURL: "https://x", Model: "m", Variants: map[string]Variant{"high": {Model: "m2"}}}, // collides with a thinking level
	}
	for i, p := range bad {
		if err := Validate(piCfg(p)); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
