package provider

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// An antigravity launch selects the provider's own gemini_dir and forces agy
// onto the credential file inside it, so two providers can hold two accounts
// at once.
func TestAntigravityLaunch(t *testing.T) {
	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/agy-a", Model: "gemini-3.8-flash-high"}
	l, err := agyEngine{}.Launch("a", p, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--gemini_dir", "/tmp/agy-a", "--model", "gemini-3.8-flash-high"}
	if strings.Join(l.Args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %q, want %q", l.Args, want)
	}
	env := envMap(l.Env)
	if env[EnvSSHConnection] != AntigravitySSHConnection {
		t.Errorf("%s = %q, want %q", EnvSSHConnection, env[EnvSSHConnection], AntigravitySSHConnection)
	}
	if env["AK_PROVIDER"] != "a" {
		t.Errorf("AK_PROVIDER = %q", env["AK_PROVIDER"])
	}
	// No key travels: agy signs in itself.
	for _, kv := range l.Env.Set {
		if kv.Secret {
			t.Errorf("%s marked secret; antigravity has no key", kv.Key)
		}
	}
}

// The gemini_dir is a leading tilde in the config; the launch expands it so the
// engine never depends on shell expansion.
func TestAntigravityExpandsGeminiDir(t *testing.T) {
	l, err := agyEngine{}.Launch("a", config.Provider{Kind: config.KindAntigravity, GeminiDir: "~/agy-a"}, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Args) != 2 || l.Args[0] != "--gemini_dir" || strings.HasPrefix(l.Args[1], "~") {
		t.Errorf("args = %q", l.Args)
	}
}

// A provider env wins over the switch ak sets, so a user who knows what they
// are doing can turn it off.
func TestAntigravityEnvOverride(t *testing.T) {
	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/a", Env: map[string]string{EnvSSHConnection: "10.0.0.1 1 10.0.0.1 22"}}
	l, err := agyEngine{}.Launch("a", p, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(l.Env)[EnvSSHConnection]; got != "10.0.0.1 1 10.0.0.1 22" {
		t.Errorf("%s = %q", EnvSSHConnection, got)
	}
}

// An effort level is a built-in variant; a custom one may set the model, the
// effort, or both.
func TestAntigravityVariants(t *testing.T) {
	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/a",
		Variants: map[string]config.Variant{"fast": {Model: "gemini-3.8-flash-low", Reasoning: "low", Shim: true}}}

	l, err := agyEngine{}.Launch("a", p, Literal(""), Context{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, v := range l.Variants {
		got[v.Name] = v.Args
	}
	for _, level := range config.EffortLevels() {
		want := "--effort " + level
		if strings.Join(got[level], " ") != want {
			t.Errorf("variant %q args = %q, want %q", level, got[level], want)
		}
	}
	if s := strings.Join(got["fast"], " "); s != "--model gemini-3.8-flash-low --effort low" {
		t.Errorf("custom variant args = %q", s)
	}
}

// gemini_dir holds the user's own data, so it must never be reclaimed the way a
// generated codex profile is.
func TestAntigravityHasNoArtifactDirs(t *testing.T) {
	if dirs := (agyEngine{}).ArtifactDirs(Context{}); len(dirs) != 0 {
		t.Errorf("ArtifactDirs = %q, want none", dirs)
	}
}
