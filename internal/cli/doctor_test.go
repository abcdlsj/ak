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
