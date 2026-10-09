package ui

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// A chosen preset pre-fills the draft; its env survives into the provider.
func TestApplyPreset(t *testing.T) {
	d := NewDraft("")
	d.Kind, d.Preset = string(config.KindClaude), "minimax"
	d.ApplyPreset()
	p := d.Provider()
	if d.Name != "minimax" || p.BaseURL != "https://api.minimax.cn/anthropic" || p.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] == "" {
		t.Errorf("draft %+v, provider %+v", d, p)
	}
	if !strings.Contains(d.keyURL, "minimax") {
		t.Errorf("keyURL = %q", d.keyURL)
	}

	// No preset leaves the draft as it was, engine aside.
	d = NewDraft("x")
	d.Kind = string(config.KindPi)
	d.ApplyPreset()
	if p := d.Provider(); p.Kind != config.KindPi || p.BaseURL != "" || d.Name != "x" {
		t.Errorf("no preset: %+v", p)
	}
}
