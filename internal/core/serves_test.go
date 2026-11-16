package core

import (
	"reflect"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// pools is a config with a plain provider, two members and a pool over them.
func pools() *config.Config {
	return &config.Config{
		Version: config.Version,
		Providers: map[string]config.Provider{
			"plain": {Kind: config.KindCodex, BaseURL: "https://a.example", Model: "m-plain"},
			"a":     {Kind: config.KindCodex, BaseURL: "https://a.example", Model: "m-a"},
			"b":     {Kind: config.KindCodex, BaseURL: "https://b.example", Model: "m-b"},
			"pool":  {Kind: config.KindCodex, Members: []string{"a", "b"}},
		},
	}
}

func names(served []Served) []string {
	out := make([]string, 0, len(served))
	for _, s := range served {
		out = append(out, s.Model)
	}
	return out
}

// A provider answers with its own model; a pool with its members', in the
// order they would answer.
func TestServes(t *testing.T) {
	cfg := pools()
	if got := names(Serves(cfg, "plain", nil)); !reflect.DeepEqual(got, []string{"m-plain"}) {
		t.Errorf("plain serves %v", got)
	}
	if got := names(Serves(cfg, "pool", nil)); !reflect.DeepEqual(got, []string{"m-a", "m-b"}) {
		t.Errorf("pool serves %v, want both members' models", got)
	}
}

// A mapping is what that member is asked for, and one deeper down wins.
func TestServesMapping(t *testing.T) {
	cfg := pools()
	cfg.Providers["pool"] = config.Provider{Kind: config.KindCodex, Members: []string{"a", "b"},
		MemberModels: map[string]string{"b": "m-mapped"}}
	if got := names(Serves(cfg, "pool", nil)); !reflect.DeepEqual(got, []string{"m-a", "m-mapped"}) {
		t.Errorf("mapped pool serves %v", got)
	}

	cfg.Providers["inner"] = config.Provider{Kind: config.KindCodex, Members: []string{"a"},
		MemberModels: map[string]string{"a": "m-inner"}}
	cfg.Providers["outer"] = config.Provider{Kind: config.KindCodex, Members: []string{"inner"},
		MemberModels: map[string]string{"inner": "m-outer"}}
	if got := names(Serves(cfg, "outer", nil)); !reflect.DeepEqual(got, []string{"m-inner"}) {
		t.Errorf("nested pool serves %v, want the deeper mapping", got)
	}
}

// A member naming no model takes the request's, which a pool that names one
// passes down.
func TestServesInheritsTheAskedModel(t *testing.T) {
	cfg := pools()
	cfg.Providers["mute"] = config.Provider{Kind: config.KindCodex, BaseURL: "https://m.example"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindCodex, Model: "m-pool", Members: []string{"mute"}}
	if got := names(Serves(cfg, "pool", nil)); !reflect.DeepEqual(got, []string{"m-pool"}) {
		t.Errorf("pool serves %v, want its own model passed to the silent member", got)
	}
}

// A name a remote service chose is replaced by the models it said it stands
// for; a name the service says nothing about stays as it is.
func TestServesThroughRemoteRoutes(t *testing.T) {
	cfg := pools()
	cfg.Providers["remote"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://ak.example", Model: "main"}
	route := func(model string) []string {
		if model == "main" {
			return []string{"deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash"}
		}
		return nil
	}
	got := names(Serves(cfg, "remote", route))
	if !reflect.DeepEqual(got, []string{"deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash"}) {
		t.Errorf("remote serves %v, want the models behind the route", got)
	}
	if got := names(Serves(cfg, "remote", nil)); !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("without an answer serves %v, want the name as it is", got)
	}
}

// The same model reached twice is answered once.
func TestServesDeduplicates(t *testing.T) {
	cfg := pools()
	cfg.Providers["twice"] = config.Provider{Kind: config.KindCodex, Members: []string{"a", "plain"},
		MemberModels: map[string]string{"plain": "m-a"}}
	if got := names(Serves(cfg, "twice", nil)); !reflect.DeepEqual(got, []string{"m-a"}) {
		t.Errorf("serves %v, want one entry", got)
	}
}
