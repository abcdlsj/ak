package cli

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestQuotaTargets(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://b"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"a", "b"}}

	// No names: pools expand to members, nothing is listed twice.
	got, err := quotaTargets(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("all = %v, want [a b]", got)
	}

	// A pool expands even when its members are also named.
	got, err = quotaTargets(cfg, []string{"pool", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("pool+a = %v, want the two members once", got)
	}

	if _, err := quotaTargets(cfg, []string{"missing"}); err == nil {
		t.Fatal("an unknown name was accepted")
	}
}

func TestParseMemberModels(t *testing.T) {
	m, err := parseMemberModels([]string{"a=m1", "b = m2"})
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != "m1" || m["b"] != "m2" {
		t.Errorf("parsed = %v", m)
	}
	if got, err := parseMemberModels(nil); err != nil || got != nil {
		t.Errorf("empty = %v, %v; want nil, nil", got, err)
	}
	for _, bad := range []string{"a", "=m", "a=", ""} {
		if _, err := parseMemberModels([]string{bad}); err == nil {
			t.Errorf("parseMemberModels(%q) accepted a malformed mapping", bad)
		}
	}
}

func TestParseMemberList(t *testing.T) {
	got := parseMemberList([]string{" a ", "", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("parseMemberList = %v", got)
	}
}
