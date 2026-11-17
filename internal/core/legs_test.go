package core

import (
	"reflect"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/vendor"
)

func TestProviderLegs(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code", APIKey: "k1"}
	cfg.Accounts["od"] = config.Account{Vendor: "opendesign", APIKey: "k2"}
	cfg.Providers["a"] = config.Provider{Kind: config.KindCodex, Account: "goat", Model: "m-a"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://b.example/v1", APIKey: "k3", WireAPI: "chat", Model: "m-b"}
	cfg.Providers["inner"] = config.Provider{
		Kind:         config.KindCodex,
		Members:      []string{"a", "od/deepseek-v4"},
		MemberModels: map[string]string{"a": "m-override"},
	}
	cfg.Providers["outer"] = config.Provider{
		Kind:         config.KindCodex,
		Model:        "logical",
		Members:      []string{"inner", "b"},
		MemberModels: map[string]string{"b": "m-outer-b"},
	}
	cfg.Providers["agy-work"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/agy-work", Model: "gemini-3"}
	cfg.Providers["cx-native"] = config.Provider{Kind: config.KindCodex, CodexHome: "/tmp/cx-native"}
	cfg.Resolve()

	legs := ProviderLegs(cfg, "outer", nil)
	if len(legs) != 3 {
		t.Fatalf("outer legs = %+v, want 3", legs)
	}
	if legs[0].Member != "a" || legs[0].Account != "goat" || legs[0].Model != "m-override" || legs[0].Protocol != vendor.ProtocolResponses {
		t.Errorf("leg 0 = %+v", legs[0])
	}
	if legs[1].Member != "od/deepseek-v4" || legs[1].Account != "od" || legs[1].Model != "deepseek-v4" {
		t.Errorf("leg 1 = %+v", legs[1])
	}
	if legs[2].Member != "b" || legs[2].Model != "m-outer-b" || legs[2].Protocol != vendor.ProtocolChat || legs[2].Endpoint != "https://b.example/v1" {
		t.Errorf("leg 2 = %+v", legs[2])
	}

	agyLegs := ProviderLegs(cfg, "agy-work", nil)
	if len(agyLegs) != 1 || agyLegs[0].Protocol != "antigravity" || agyLegs[0].Endpoint != "/tmp/agy-work" {
		t.Errorf("agy legs = %+v", agyLegs)
	}

	nativeLegs := ProviderLegs(cfg, "cx-native", nil)
	if len(nativeLegs) != 1 || nativeLegs[0].Protocol != "native" || nativeLegs[0].Endpoint != "/tmp/cx-native" {
		t.Errorf("native legs = %+v", nativeLegs)
	}
}

func TestProviderLegsRemoteServes(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["remote-claude"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://ak.example", APIKey: "t", Model: "main"}
	route := func(m string) []string {
		if m == "main" {
			return []string{"glm-5", "deepseek-v4"}
		}
		return nil
	}
	legs := ProviderLegs(cfg, "remote-claude", route)
	if len(legs) != 1 || !reflect.DeepEqual(legs[0].Serves, []string{"glm-5", "deepseek-v4"}) {
		t.Errorf("legs = %+v", legs)
	}
}

func TestRouteLegs(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code", APIKey: "k1", Model: "deepseek/deepseek-v4.1-flash"}
	cfg.Accounts["od"] = config.Account{Vendor: "opendesign", APIKey: "k2"}
	cfg.Service.Routes = map[string]config.Route{
		"fast": {Model: "goat"},
		"main": {Members: []string{"fast", "od/deepseek-v4-pro"}, Strategy: config.StrategySmart},
	}
	cfg.Resolve()

	legs, err := RouteLegs(cfg, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 2 {
		t.Fatalf("RouteLegs = %+v, want 2", legs)
	}
	if legs[0].Account != "goat" || legs[0].Model != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("leg 0 = %+v", legs[0])
	}
	if legs[1].Account != "od" || legs[1].Model != "deepseek-v4-pro" {
		t.Errorf("leg 1 = %+v", legs[1])
	}
	if legs[0].Protocols[vendor.ProtocolAnthropic] != "native" {
		t.Errorf("protocols = %+v", legs[0].Protocols)
	}
}
