package ui

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// editDraftOf edits p as provider "pool" of an otherwise empty config.
func editDraftOf(p config.Provider) *Draft {
	cfg := config.Default()
	cfg.Providers["pool"] = p
	return EditDraft(cfg, "pool")
}

func TestDraftPoolRoundTrip(t *testing.T) {
	src := config.Provider{
		Kind: config.KindClaude, Members: []string{"a", "b"},
		Strategy: config.StrategyRotate, MemberModels: map[string]string{"a": "m1"},
		Model: "logical",
	}
	d := editDraftOf(src)
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
	d := editDraftOf(config.Provider{
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
	d := editDraftOf(config.Provider{
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

// A pool can say what to ask each member for, in the form as on the command
// line: the chosen model is what the member is asked, not the member's own.
func TestDraftPoolMemberModels(t *testing.T) {
	d := NewDraft("p")
	d.Kind = string(config.KindCodex)
	d.Members = "a, b"
	d.Maps = "a=deepseek-v4.1-flash, b=claude-sonnet-5-5"
	got := d.Provider()
	if got.MemberModels["a"] != "deepseek-v4.1-flash" || got.MemberModels["b"] != "claude-sonnet-5-5" {
		t.Fatalf("mappings = %v", got.MemberModels)
	}
	// A pair naming a model the member does not have is a typo worth keeping
	// out of the file; a pair naming a member the pool does not have is
	// dropped.
	d.Maps = "a=m1, c=m3"
	if got := d.Provider(); len(got.MemberModels) != 1 || got.MemberModels["a"] != "m1" {
		t.Errorf("mappings = %v, want only a=m1", got.MemberModels)
	}
}

func TestDraftPoolMappingRoundTrip(t *testing.T) {
	d := editDraftOf(config.Provider{
		Kind: config.KindCodex, Members: []string{"a", "b"},
		MemberModels: map[string]string{"b": "m2", "a": "m1"},
	})
	if d.Maps != "a=m1, b=m2" {
		t.Fatalf("Maps = %q, want the pairs sorted", d.Maps)
	}
}
