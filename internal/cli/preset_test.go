package cli

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/spf13/cobra"
)

// addWith returns an add command with the given flags set, as if passed.
func addWith(t *testing.T, flags map[string]string) *cobra.Command {
	t.Helper()
	cmd := newAddCmd()
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return cmd
}

// Flags passed with --preset override it; flags not passed leave it alone.
func TestPresetProviderOverrides(t *testing.T) {
	cmd := addWith(t, map[string]string{"kind": "codex", "model": "kimi-k9", "key": "sk-x"})
	p, err := presetProvider(cmd, "kimi")
	if err != nil {
		t.Fatal(err)
	}
	want := config.Provider{Kind: config.KindCodex, Display: "Kimi", BaseURL: "https://api.moonshot.cn/v1",
		Model: "kimi-k9", APIKey: "sk-x", WireAPI: "responses"}
	if p.Kind != want.Kind || p.Display != want.Display || p.BaseURL != want.BaseURL ||
		p.Model != want.Model || p.APIKey != want.APIKey || p.WireAPI != want.WireAPI {
		t.Errorf("got %+v, want %+v", p, want)
	}

	// The default --kind (claude) is not passed, so a single-engine preset
	// keeps its own.
	p, err = presetProvider(addWith(t, map[string]string{"key": "env:K"}), "command-code")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != config.KindCodex || p.APIKeyRef != "env:K" || p.BaseURL == "" {
		t.Errorf("command-code = %+v", p)
	}

	p, err = presetProvider(addWith(t, map[string]string{"kind": "claude", "opus": "big"}), "kimi-coding")
	if err != nil {
		t.Fatal(err)
	}
	if p.Opus != "big" || p.Model != "kimi-for-coding" || p.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] == "" {
		t.Errorf("kimi-coding = %+v", p)
	}
}

func TestPresetProviderErrors(t *testing.T) {
	cases := map[string]struct {
		id    string
		flags map[string]string
		want  string
	}{
		"ambiguous":  {"kimi", nil, "exists for claude, codex, pi; pass --kind"},
		"wrong kind": {"command-code", map[string]string{"kind": "pi"}, "has no pi version; it exists for codex"},
		"unknown":    {"nope", nil, "unknown preset"},
	}
	for name, c := range cases {
		_, err := presetProvider(addWith(t, c.flags), c.id)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
