package provider

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// EnvAgentHome is the one variable a runtime that watches agent sessions reads
// to learn where this session keeps its engine state, whichever engine it is.
// Each engine reports it the same way, from the field that relocates its home.
func TestAgentHomeIsReported(t *testing.T) {
	cases := []struct {
		name string
		eng  Engine
		p    config.Provider
		home string
	}{
		{
			name: "claude config_dir",
			eng:  claudeEngine{},
			p:    config.Provider{Kind: config.KindClaude, BaseURL: "https://a", ConfigDir: "/tmp/claude-work"},
			home: "/tmp/claude-work",
		},
		{
			name: "codex home",
			eng:  codexEngine{},
			p:    config.Provider{Kind: config.KindCodex, BaseURL: "https://a", CodexHome: "/tmp/codex-work"},
			home: "/tmp/codex-work",
		},
		{
			name: "agy gemini_dir",
			eng:  agyEngine{},
			p:    config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/agy-work"},
			home: "/tmp/agy-work",
		},
	}
	for _, c := range cases {
		l, err := c.eng.Launch("a", c.p, Literal("k"), Context{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := envMap(l.Env)[EnvAgentHome]; got != c.home {
			t.Errorf("%s: %s = %q, want %q", c.name, EnvAgentHome, got, c.home)
		}
	}
}

// A provider on the shared home says nothing, so the watcher's own default
// applies and ak does not have to mirror every engine's default directory.
func TestAgentHomeIsOmittedWhenShared(t *testing.T) {
	for _, p := range []config.Provider{
		{Kind: config.KindClaude, BaseURL: "https://a"},
		{Kind: config.KindCodex, BaseURL: "https://a"},
		{Kind: config.KindPi, BaseURL: "https://a", Model: "m"},
	} {
		eng := EngineFor(p.Kind)
		l, err := eng.Launch("a", p, Literal("k"), Context{})
		if err != nil {
			t.Fatalf("%s: %v", p.Kind, err)
		}
		if got, ok := envMap(l.Env)[EnvAgentHome]; ok {
			t.Errorf("%s on the shared home reports %q", p.Kind, got)
		}
	}
}

// A tilde is expanded here, once, so the watcher never has to know how ak's
// config spells a path.
func TestAgentHomeExpandsTilde(t *testing.T) {
	l, err := agyEngine{}.Launch("a", config.Provider{Kind: config.KindAntigravity, GeminiDir: "~/agy"}, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	home := envMap(l.Env)[EnvAgentHome]
	if home == "" || home[0] == '~' {
		t.Errorf("%s = %q, want an absolute path", EnvAgentHome, home)
	}
}

// The provider's own env merges last, so a user who needs to state the home
// differently can, without ak fighting them.
func TestAgentHomeFollowsProviderEnv(t *testing.T) {
	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/a",
		Env: map[string]string{EnvAgentHome: "/tmp/elsewhere"}}
	l, err := agyEngine{}.Launch("a", p, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(l.Env)[EnvAgentHome]; got != "/tmp/elsewhere" {
		t.Errorf("%s = %q", EnvAgentHome, got)
	}
}
