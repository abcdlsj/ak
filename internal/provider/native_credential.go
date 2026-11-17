package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// NativeAuth summarizes an engine's own local credential state without ever
// querying the OS keychain or making a network request.
type NativeAuth struct {
	SignedIn bool
	Account  string
	// State is ok, expiring, expired, missing, or unknown.
	State string
}

// ReadNativeAuth inspects the isolated directory of a native-login provider to
// report whether it is signed in and as whom.
func ReadNativeAuth(p config.Provider) NativeAuth {
	switch p.Kind {
	case config.KindAntigravity:
		if p.GeminiDir == "" {
			return NativeAuth{State: "missing"}
		}
		t, err := ReadAntigravityToken(config.ExpandHome(p.GeminiDir))
		if err != nil {
			return NativeAuth{State: "missing"}
		}
		return NativeAuth{SignedIn: true, Account: t.Email, State: t.State(time.Now())}

	case config.KindCodex:
		if p.CodexHome == "" {
			return NativeAuth{State: "unknown"}
		}
		return readCodexNativeAuth(config.ExpandHome(p.CodexHome))

	case config.KindClaude:
		if p.ConfigDir == "" {
			return NativeAuth{State: "unknown"}
		}
		return readClaudeNativeAuth(config.ExpandHome(p.ConfigDir))

	case config.KindPi:
		if p.PiDir == "" {
			if p.PiProvider != "" {
				return NativeAuth{SignedIn: true, Account: p.PiProvider, State: "ok"}
			}
			return NativeAuth{State: "unknown"}
		}
		return readPiNativeAuth(config.ExpandHome(p.PiDir), p.PiProvider)
	}
	return NativeAuth{State: "unknown"}
}

func readCodexNativeAuth(codexHome string) NativeAuth {
	raw, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		return NativeAuth{State: "missing"}
	}
	var doc struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return NativeAuth{State: "missing"}
	}
	if doc.Tokens.AccessToken == "" && doc.Tokens.RefreshToken == "" && doc.APIKey == "" {
		return NativeAuth{State: "missing"}
	}
	return NativeAuth{
		SignedIn: true,
		Account:  emailFromIDToken(doc.Tokens.IDToken),
		State:    "ok",
	}
}

func readClaudeNativeAuth(configDir string) NativeAuth {
	var account string
	var hasAccount bool
	if raw, err := os.ReadFile(filepath.Join(configDir, ".claude.json")); err == nil {
		var doc struct {
			OAuthAccount struct {
				EmailAddress string `json:"emailAddress"`
				AccountUUID  string `json:"accountUuid"`
			} `json:"oauthAccount"`
		}
		if json.Unmarshal(raw, &doc) == nil && (doc.OAuthAccount.EmailAddress != "" || doc.OAuthAccount.AccountUUID != "") {
			account = doc.OAuthAccount.EmailAddress
			hasAccount = true
		}
	}
	var hasCred bool
	if raw, err := os.ReadFile(filepath.Join(configDir, ".credentials.json")); err == nil {
		var doc struct {
			ClaudeAiOauth struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(raw, &doc) == nil && (doc.ClaudeAiOauth.AccessToken != "" || doc.ClaudeAiOauth.RefreshToken != "") {
			hasCred = true
		}
	}
	if hasCred || hasAccount {
		return NativeAuth{SignedIn: true, Account: account, State: "ok"}
	}
	// On macOS, Claude Code stores OAuth tokens in the Keychain keyed by a
	// hash of CLAUDE_CONFIG_DIR; without querying Keychain, an empty dir may
	// still hold or await interactive sign-in.
	return NativeAuth{State: "unknown"}
}

func readPiNativeAuth(piDir, piProvider string) NativeAuth {
	raw, err := os.ReadFile(filepath.Join(piDir, "auth.json"))
	if err != nil {
		return NativeAuth{State: "missing"}
	}
	var entries map[string]struct {
		Type    string `json:"type"`
		Key     string `json:"key"`
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return NativeAuth{State: "missing"}
	}
	hasToken := func(id string) bool {
		e, ok := entries[id]
		return ok && (e.Key != "" || e.Access != "" || e.Refresh != "")
	}
	if piProvider != "" {
		if hasToken(piProvider) {
			return NativeAuth{SignedIn: true, Account: piProvider, State: "ok"}
		}
		return NativeAuth{State: "missing"}
	}
	var signed []string
	for id := range entries {
		if hasToken(id) {
			signed = append(signed, id)
		}
	}
	sort.Strings(signed)
	if len(signed) == 0 {
		return NativeAuth{State: "missing"}
	}
	return NativeAuth{SignedIn: true, Account: strings.Join(signed, ", "), State: "ok"}
}
