package preset

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

var idRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Every preset, given a key, makes a provider the config accepts.
func TestPresetsValidate(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("no presets")
	}
	for _, ps := range All() {
		var p config.Provider
		ps.Apply(&p)
		p.SetKey("sk-test")
		cfg := config.Default()
		cfg.Providers[ps.ID] = p
		if err := config.Validate(cfg); err != nil {
			t.Errorf("%s/%s: %v", ps.ID, ps.Kind, err)
		}
		if ps.Name == "" || ps.Category == "" || ps.BaseURL == "" {
			t.Errorf("%s/%s: missing name, category or base_url", ps.ID, ps.Kind)
		}
	}
}

func TestPresetIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, ps := range All() {
		if !idRe.MatchString(ps.ID) {
			t.Errorf("id %q is not a kebab-case slug", ps.ID)
		}
		k := ps.ID + "/" + string(ps.Kind)
		if seen[k] {
			t.Errorf("duplicate preset %s", k)
		}
		seen[k] = true
	}
}

// URLs carry no affiliate or tracking parameters.
func TestPresetURLsClean(t *testing.T) {
	tracking := []string{"aff", "track_id", "ref", "invite", "ic", "ac", "rc"}
	for _, ps := range All() {
		for _, raw := range []string{ps.BaseURL, ps.WebsiteURL, ps.APIKeyURL} {
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" {
				t.Errorf("%s/%s: bad url %q", ps.ID, ps.Kind, raw)
				continue
			}
			q := u.Query()
			for _, k := range tracking {
				if q.Has(k) {
					t.Errorf("%s/%s: url %q carries %s=", ps.ID, ps.Kind, raw, k)
				}
			}
			for k := range q {
				if strings.HasPrefix(k, "utm_") {
					t.Errorf("%s/%s: url %q carries %s=", ps.ID, ps.Kind, raw, k)
				}
			}
		}
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("kimi", ""); ok {
		t.Error("kimi has several engines; an empty kind must not match")
	}
	p, ok := Lookup("kimi", config.KindCodex)
	if !ok || p.Kind != config.KindCodex || p.WireAPI != "responses" {
		t.Errorf("Lookup(kimi, codex) = %+v, %v", p, ok)
	}
	if p, ok := Lookup("command-code", ""); !ok || p.Kind != config.KindCodex {
		t.Errorf("Lookup(command-code) = %+v, %v; want the only engine", p, ok)
	}
	if _, ok := Lookup("nope", config.KindClaude); ok {
		t.Error("unknown id matched")
	}
	for _, k := range []config.Kind{config.KindClaude, config.KindCodex, config.KindPi} {
		for _, p := range ForKind(k) {
			if p.Kind != k {
				t.Errorf("ForKind(%s) returned %s/%s", k, p.ID, p.Kind)
			}
		}
	}
}

// Apply keeps the provider's own env entries and display name.
func TestApplyKeepsOwn(t *testing.T) {
	ps, _ := Lookup("kimi-coding", config.KindClaude)
	p := config.Provider{Display: "mine", Env: map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "1"}}
	ps.Apply(&p)
	if p.Display != "mine" || p.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1" || p.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] == "" {
		t.Errorf("Apply = %+v", p)
	}
	if ps.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] == "1" {
		t.Error("Apply wrote into the preset's own env")
	}
}
