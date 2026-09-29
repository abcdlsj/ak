package ui

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestDraftPoolRoundTrip(t *testing.T) {
	src := config.Provider{
		Kind: config.KindClaude, Members: []string{"a", "b"},
		Strategy: config.StrategyRotate, MemberModels: map[string]string{"a": "m1"},
		Model: "logical",
	}
	d := EditDraft("pool", src)
	if d.Members != "a, b" {
		t.Fatalf("Members = %q, want \"a, b\"", d.Members)
	}
	got := d.Provider()
	if !got.IsPool() || len(got.Members) != 2 || got.Strategy != config.StrategyRotate {
		t.Fatalf("round trip lost the pool: %+v", got)
	}
	if got.BaseURL != "" {
		t.Errorf("pool kept an endpoint: %q", got.BaseURL)
	}
	if got.MemberModels["a"] != "m1" {
		t.Errorf("mapping lost: %v", got.MemberModels)
	}
}

func TestDraftPoolDropsRemovedMapping(t *testing.T) {
	d := EditDraft("pool", config.Provider{
		Kind: config.KindClaude, Members: []string{"a", "b"},
		MemberModels: map[string]string{"a": "m1", "b": "m2"},
	})
	d.Members = "a"
	got := d.Provider()
	if _, ok := got.MemberModels["b"]; ok {
		t.Errorf("mapping for a removed member survived: %v", got.MemberModels)
	}
	if got.MemberModels["a"] != "m1" {
		t.Errorf("mapping for a kept member was lost: %v", got.MemberModels)
	}
}

func TestDraftBlankMembersClearsPool(t *testing.T) {
	d := EditDraft("pool", config.Provider{
		Kind: config.KindClaude, Members: []string{"a"}, Strategy: config.StrategyOrder,
		MemberModels: map[string]string{"a": "m1"},
	})
	d.Members = ""
	d.BaseURL = "https://up.example"
	got := d.Provider()
	if got.IsPool() || got.Strategy != "" || got.MemberModels != nil {
		t.Errorf("pool fields survived a blank member list: %+v", got)
	}
	if got.BaseURL != "https://up.example" {
		t.Errorf("endpoint = %q", got.BaseURL)
	}
}
