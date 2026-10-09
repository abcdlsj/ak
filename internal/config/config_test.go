package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnknownKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := Path()
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("version = 1\n[providers.a]\nkind = 'claude'\nbase_url = 'x'\nmodle = 'm'\n"), 0o600)
	keys, err := UnknownKeys()
	if err != nil || !reflect.DeepEqual(keys, []string{"providers.a.modle"}) {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
}

// A default_variant must name a variant the provider's command recognises.
func TestValidateDefaultVariant(t *testing.T) {
	ok := []Provider{
		{Kind: KindPi, BaseURL: "https://x", Model: "m", DefaultVariant: "high"},
		{Kind: KindCodex, BaseURL: "https://x", DefaultVariant: "xhigh"},
		{Kind: KindClaude, BaseURL: "https://x", Model: "m", DefaultVariant: "opus"},
		{Kind: KindPi, BaseURL: "https://x", Model: "m", DefaultVariant: "fast",
			Variants: map[string]Variant{"fast": {Model: "m2"}}},
		{Kind: KindClaude, BaseURL: "https://x", Model: "m", DefaultVariant: "unsure",
			Variants: map[string]Variant{"unsure": {Model: "m2"}}},
	}
	for i, p := range ok {
		if err := Validate(piCfg(p)); err != nil {
			t.Errorf("case %d rejected: %v", i, err)
		}
	}

	bad := []Provider{
		{Kind: KindPi, BaseURL: "https://x", Model: "m", DefaultVariant: "fast"},     // unknown for pi
		{Kind: KindCodex, BaseURL: "https://x", DefaultVariant: "off"},               // pi level, not codex
		{Kind: KindClaude, BaseURL: "https://x", Model: "m", DefaultVariant: "high"}, // reasoning level, not a tier
	}
	for i, p := range bad {
		if err := Validate(piCfg(p)); err == nil {
			t.Errorf("case %d accepted an unknown default_variant", i)
		}
	}
}
