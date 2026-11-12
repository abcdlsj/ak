package cli

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func TestKeyRefProblems(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["good"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKeyRef: "cmd:printf sk-ok"}
	cfg.Providers["bad"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://x", APIKeyRef: "env:AK_DOCTOR_MISSING_XYZ"}
	got := keyRefProblems(cfg)
	if len(got) != 1 || !strings.Contains(got[0], "bad") {
		t.Errorf("keyRefProblems = %v, want only bad", got)
	}
	if !hasKeyRef(cfg) {
		t.Error("hasKeyRef = false with a referenced key")
	}
	if hasKeyRef(config.Default()) {
		t.Error("hasKeyRef = true with no referenced key")
	}
}

// The result line counts the problems and the warnings; the command fails only
// when there are problems.
func TestDoctorSummary(t *testing.T) {
	cases := []struct {
		problems, warnings int
		want               string
	}{
		{0, 0, "No problems"},
		{0, 2, "No problems · 2 warnings"},
		{1, 0, "1 problem"},
		{3, 1, "3 problems · 1 warning"},
	}
	for _, c := range cases {
		d := &doctor{problems: c.problems, warnings: c.warnings}
		if got := d.summary(); got != c.want {
			t.Errorf("summary(%d problems, %d warnings) = %q, want %q", c.problems, c.warnings, got, c.want)
		}
	}
	if (&doctor{}).err() != nil {
		t.Error("no problem must not fail")
	}
	if err := (&doctor{problems: 1}).err(); err == nil || !strings.Contains(err.Error(), "1 problem needs") {
		t.Errorf("err = %v", err)
	}
}

// A hint is recorded once, however many checks report it.
func TestDoctorHintOnce(t *testing.T) {
	d := &doctor{}
	d.hint("Run `ak sync`")
	d.hint("Run `ak sync`")
	if len(d.hints) != 1 {
		t.Errorf("hints = %q", d.hints)
	}
}
