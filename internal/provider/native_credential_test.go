package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

func fakeJWT(t *testing.T, email string) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]string{"email": email})
	return hdr + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestReadNativeAuth(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		dir := t.TempDir()
		p := config.Provider{Kind: config.KindCodex, CodexHome: dir}
		if got := ReadNativeAuth(p); got.SignedIn || got.State != "missing" {
			t.Fatalf("missing auth.json = %+v", got)
		}
		doc := map[string]any{
			"tokens": map[string]string{
				"access_token":  "at",
				"refresh_token": "rt",
				"id_token":      fakeJWT(t, "work@example.com"),
			},
		}
		b, _ := json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		got := ReadNativeAuth(p)
		if !got.SignedIn || got.Account != "work@example.com" || got.State != "ok" {
			t.Fatalf("signed in codex = %+v", got)
		}
	})

	t.Run("claude", func(t *testing.T) {
		dir := t.TempDir()
		p := config.Provider{Kind: config.KindClaude, ConfigDir: dir}
		if got := ReadNativeAuth(p); got.SignedIn || got.State != "unknown" {
			t.Fatalf("empty config_dir = %+v", got)
		}
		b := []byte(`{"oauthAccount":{"emailAddress":"me@anthropic.example","accountUuid":"u1"}}`)
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		got := ReadNativeAuth(p)
		if !got.SignedIn || got.Account != "me@anthropic.example" || got.State != "ok" {
			t.Fatalf("signed in claude = %+v", got)
		}
	})

	t.Run("pi", func(t *testing.T) {
		dir := t.TempDir()
		p := config.Provider{Kind: config.KindPi, PiDir: dir}
		if got := ReadNativeAuth(p); got.SignedIn || got.State != "missing" {
			t.Fatalf("missing pi auth.json = %+v", got)
		}
		b := []byte(`{"openai-codex":{"type":"oauth","access":"a","refresh":"r"},"anthropic":{"type":"oauth","access":"a2","refresh":"r2"}}`)
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		got := ReadNativeAuth(p)
		if !got.SignedIn || got.Account != "anthropic, openai-codex" || got.State != "ok" {
			t.Fatalf("signed in pi = %+v", got)
		}
	})

	t.Run("agy", func(t *testing.T) {
		dir := t.TempDir()
		p := config.Provider{Kind: config.KindAntigravity, GeminiDir: dir}
		if got := ReadNativeAuth(p); got.SignedIn || got.State != "missing" {
			t.Fatalf("missing agy token = %+v", got)
		}
		raw := []byte(`{"token":{"access_token":"ya29.a","refresh_token":"1//r"},"id_token_email":"g@gmail.com"}`)
		if err := WriteAntigravityToken(dir, raw); err != nil {
			t.Fatal(err)
		}
		got := ReadNativeAuth(p)
		if !got.SignedIn || got.Account != "g@gmail.com" || got.State != "ok" {
			t.Fatalf("signed in agy = %+v", got)
		}
	})
}
