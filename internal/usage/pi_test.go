package usage

import (
	"strconv"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func piAssistant(id, iso string, ts int64, provider, model string, in, out, cr, cw, reasoning int64) string {
	return `{"type":"message","id":"` + id + `","timestamp":"` + iso + `","message":{` +
		`"role":"assistant","provider":"` + provider + `","model":"` + model + `",` +
		`"timestamp":` + piItoa(ts) + `,"usage":{"input":` + piItoa(in) + `,"output":` + piItoa(out) +
		`,"cacheRead":` + piItoa(cr) + `,"cacheWrite":` + piItoa(cw) + `,"reasoning":` + piItoa(reasoning) +
		`,"totalTokens":` + piItoa(in+out+cr+cw) + `}}}`
}

func piItoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestPiParserAssistantMessage(t *testing.T) {
	p := piSource{}.NewParser(nil)
	line := piAssistant("b2c3", "2026-09-01T10:00:02.000Z", 1756720802000, "ak-relay", "glm-5", 100, 20, 50, 5, 8)
	rec, ok := p.Parse([]byte(line))
	if !ok {
		t.Fatal("an assistant message was not parsed")
	}
	if rec.Model != "glm-5" || rec.RawProvider != "ak-relay" {
		t.Errorf("record = %+v", rec)
	}
	want := Tokens{Input: 100, Output: 20, CacheRead: 50, CacheWrite: 5, Thinking: 8}
	if rec.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", rec.Tokens, want)
	}
	if rec.Time.IsZero() {
		t.Error("timestamp was not parsed")
	}
}

func TestPiParserIgnoresUserAndEmpty(t *testing.T) {
	p := piSource{}.NewParser(nil)
	if _, ok := p.Parse([]byte(`{"type":"message","id":"u","timestamp":"2026-09-01T10:00:00Z","message":{"role":"user","content":"hi"}}`)); ok {
		t.Error("a user message was counted")
	}
	if _, ok := p.Parse([]byte(`{"type":"message","id":"a","timestamp":"2026-09-01T10:00:00Z","message":{"role":"assistant","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}}`)); ok {
		t.Error("an all-zero usage was counted")
	}
}

func TestPiParserModelChangeAndUsageEntry(t *testing.T) {
	p := piSource{}.NewParser(nil)
	if _, ok := p.Parse([]byte(`{"type":"model_change","id":"m","timestamp":"2026-09-01T10:00:00Z","provider":"ak-x","modelId":"new-model"}`)); ok {
		t.Fatal("a model_change produced a record")
	}
	// A usage entry carries no model of its own and inherits the latest.
	rec, ok := p.Parse([]byte(`{"type":"usage","id":"w1","timestamp":"2026-09-01T10:00:05Z","kind":"cache_warm","provider":"ak-x","model":"new-model","usage":{"input":0,"output":0,"cacheRead":50000,"cacheWrite":0,"totalTokens":50000}}`))
	if !ok {
		t.Fatal("a usage entry was not parsed")
	}
	if rec.Model != "new-model" || rec.Tokens.CacheRead != 50000 {
		t.Errorf("record = %+v", rec)
	}
}

func TestPiParserDedupeKeyStable(t *testing.T) {
	line := piAssistant("same", "2026-09-01T10:00:02.000Z", 1756720802000, "ak-relay", "m", 1, 2, 3, 4, 0)
	a, ok := piSource{}.NewParser(nil).Parse([]byte(line))
	if !ok {
		t.Fatal("parse failed")
	}
	b, _ := piSource{}.NewParser(nil).Parse([]byte(line))
	if a.Key != b.Key || a.Key == 0 {
		t.Errorf("keys differ: %d vs %d", a.Key, b.Key)
	}
}

func TestPiProviderNames(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["relay"] = config.Provider{Kind: config.KindPi, BaseURL: "https://x", Model: "m"}
	cfg.Providers["cc"] = config.Provider{Kind: config.KindPi, PiProvider: "commandcode", Model: "m"}
	cfg.Providers["claude"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://y"}

	ids := piProviderNames(cfg)
	if ids["ak-relay"] != "relay" {
		t.Errorf("ak-relay = %q, want relay", ids["ak-relay"])
	}
	if ids["commandcode"] != "cc" {
		t.Errorf("commandcode = %q, want cc", ids["commandcode"])
	}
	if _, ok := ids["ak-claude"]; ok {
		t.Error("a claude provider was mapped as pi")
	}

	rows := attribute([]bucket{
		{Date: "2026-09-01", Engine: "pi", Model: "m", RawProvider: "ak-relay", Requests: 1},
		{Date: "2026-09-01", Engine: "pi", Model: "m", RawProvider: "someoneelse", Requests: 1},
	}, &attribution{piIDs: ids})
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Provider] = true
	}
	if !got["relay"] || !got["someoneelse"] {
		t.Errorf("rows = %v", got)
	}
}

func TestPiProviderNamesAmbiguousStaysRaw(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindPi, PiProvider: "shared", Model: "m"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindPi, PiProvider: "shared", Model: "m"}
	if ids := piProviderNames(cfg); len(ids) != 0 {
		t.Errorf("ids = %v, want a shared pi_provider left unmapped", ids)
	}
}
