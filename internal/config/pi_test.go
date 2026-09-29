package config

import "testing"

func piCfg(p Provider) *Config {
	cfg := Default()
	cfg.Providers["x"] = p
	return cfg
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
