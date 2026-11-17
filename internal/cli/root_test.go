package cli

import (
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// Every subcommand and alias is reserved, so no provider can shadow one.
func TestCommandsReserved(t *testing.T) {
	for _, c := range newRoot().Commands() {
		for _, n := range append([]string{c.Name()}, c.Aliases...) {
			if err := config.ValidateName(n); err == nil && n[0] != '_' {
				t.Errorf("%q is a command but a valid provider name", n)
			}
		}
	}
}

func TestRootGroupsAndTypoSuggestion(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["kimi"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a.example", APIKey: "k"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	root := newRoot()
	for _, c := range root.Commands() {
		if !c.Hidden && c.GroupID == "" {
			t.Errorf("visible command %q has no GroupID", c.Name())
		}
	}

	if _, err := runRoot(t, "", "statsu"); err == nil || !strings.Contains(err.Error(), `did you mean "status"`) {
		t.Errorf("typo status err = %v", err)
	}
	if _, err := runRoot(t, "", "kmi"); err == nil || !strings.Contains(err.Error(), `did you mean "kimi"`) {
		t.Errorf("typo kimi err = %v", err)
	}
}
