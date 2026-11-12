package core

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// A balance is asked once per account and once per provider of its own:
// providers on an account are their account, a pool its members'.
func TestHolders(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts["og"] = config.Account{Vendor: "opencode-go"}
	cfg.Providers["oc"] = config.Provider{Kind: config.KindClaude, Account: "og"}
	cfg.Providers["op"] = config.Provider{Kind: config.KindPi, Account: "og"}
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"a", "oc"}}
	cfg.Resolve()

	got, err := Holders(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "og" || !got[0].Account || len(got[0].Users) != 2 || got[1].Name != "a" {
		t.Fatalf("all = %+v", got)
	}
	if got[0].Provider.BaseURL == "" {
		t.Error("the account's holder has no endpoint to ask")
	}

	got, err = Holders(cfg, []string{"pool", "op"})
	if err != nil || len(got) != 2 {
		t.Fatalf("pool+op = %+v, %v", got, err)
	}
	if _, err := Holders(cfg, []string{"missing"}); err == nil {
		t.Fatal("an unknown name was accepted")
	}
}
