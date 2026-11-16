package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildIDToken makes an unsigned JWT-shaped string carrying one claim set; agy
// only ever reads the payload back.
func buildIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + "."
}

// agyDoc is the document agy writes, as far as ak reads it.
func agyDoc(t *testing.T, expiry string, extra map[string]any) []byte {
	t.Helper()
	doc := map[string]any{
		"token": map[string]any{
			"access_token":  "ya29.access",
			"token_type":    "Bearer",
			"refresh_token": "1//refresh",
			"expiry":        expiry,
		},
		"auth_method": "consumer",
	}
	for k, v := range extra {
		doc[k] = v
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAntigravityTokenPaths(t *testing.T) {
	dir := "/tmp/agy-a"
	if got, want := AntigravityTokenPath(dir), "/tmp/agy-a/antigravity-cli/antigravity-oauth-token"; got != want {
		t.Errorf("token path = %q, want %q", got, want)
	}
	if got, want := AntigravityConfigPath(dir), "/tmp/agy-a/config"; got != want {
		t.Errorf("config path = %q, want %q", got, want)
	}
}

// The Keychain holds the same document, wrapped by go-keyring; ak has to read
// it there and write it back unwrapped.
func TestAntigravityTokenParsesKeyringWrapper(t *testing.T) {
	raw := agyDoc(t, "2026-11-16T07:04:59.653889Z", nil)
	wrapped := []byte(keyringPrefix + base64.StdEncoding.EncodeToString(raw))
	tok, err := ParseAntigravityToken(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "ya29.access" || tok.RefreshToken != "1//refresh" {
		t.Errorf("token = %+v", tok)
	}
	if !tok.Expiry.Equal(time.Date(2026, 11, 16, 7, 4, 59, 653889000, time.UTC)) {
		t.Errorf("expiry = %v", tok.Expiry)
	}
}

// A document without a token is not a credential, whatever else it holds.
func TestAntigravityTokenRejectsEmpty(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"token":{}}`, `not json`} {
		if _, err := ParseAntigravityToken([]byte(raw)); err == nil {
			t.Errorf("%q parsed as a credential", raw)
		}
	}
}

// With a refresh token agy renews by itself, so an expired access token is not
// worth a warning; without one the account really is about to stop working.
func TestAntigravityTokenState(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		doc     []byte
		want    string
		wantErr bool
	}{
		{name: "fresh", doc: agyDoc(t, "2026-11-16T13:00:00Z", nil), want: "ok"},
		// The refresh token makes the expiry irrelevant.
		{name: "expired but refreshable", doc: agyDoc(t, "2026-11-15T13:00:00Z", nil), want: "ok"},
		{name: "no expiry", doc: agyDoc(t, "", nil), want: "ok"},
	}
	for _, c := range cases {
		tok, err := ParseAntigravityToken(c.doc)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := tok.State(now); got != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, got, c.want)
		}
	}

	// The same document without a refresh token is the case that matters: no
	// one can renew it.
	var doc map[string]any
	raw := agyDoc(t, "2026-11-16T13:00:00Z", nil)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	token := doc["token"].(map[string]any)
	delete(token, "refresh_token")

	if raw, err := json.Marshal(doc); err == nil {
		tok, err := ParseAntigravityToken(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := tok.State(now.Add(30 * time.Minute)); got != "expiring" {
			t.Errorf("expiring state = %q", got)
		}
		if got := tok.State(now.Add(2 * time.Hour)); got != "expired" {
			t.Errorf("expired state = %q", got)
		}
	}
}

// The address comes from the ID token agy leaves in the document, when it
// leaves one at all.
func TestAntigravityTokenEmail(t *testing.T) {
	id := buildIDToken(t, map[string]any{"email": "someone@example.com", "sub": "123"})
	raw := agyDoc(t, "2026-11-16T13:00:00Z", map[string]any{"id_token": id})
	tok, err := ParseAntigravityToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Email != "someone@example.com" {
		t.Errorf("email = %q", tok.Email)
	}

	// agy-auth writes the address beside the token for accounts it signed in.
	raw = agyDoc(t, "", map[string]any{"id_token_email": "beside@example.com"})
	if tok, err = ParseAntigravityToken(raw); err != nil || tok.Email != "beside@example.com" {
		t.Errorf("beside email = %q (%v)", tok.Email, err)
	}

	// The Keychain document this machine has carries no ID token; that is a
	// credential without a name, not a broken one.
	raw = agyDoc(t, "", nil)
	if tok, err = ParseAntigravityToken(raw); err != nil || tok.Email != "" {
		t.Errorf("anonymous email = %q (%v)", tok.Email, err)
	}
}

// Importing keeps the document byte for byte, so a field ak does not know
// (a project, a region, a tier) survives the copy.
func TestWriteAntigravityToken(t *testing.T) {
	dir := t.TempDir()
	raw := agyDoc(t, "2026-11-16T07:04:59Z", map[string]any{"project_id": "p", "region": "us", "user_tier": "t"})
	wrapped := []byte(keyringPrefix + base64.StdEncoding.EncodeToString(raw))
	if err := WriteAntigravityToken(dir, wrapped); err != nil {
		t.Fatal(err)
	}
	path := AntigravityTokenPath(dir)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("written file is not the document: %v", err)
	}
	if doc["project_id"] != "p" || doc["region"] != "us" || doc["user_tier"] != "t" {
		t.Errorf("unknown fields lost: %v", doc)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	// Reading it back is the check that the copy is usable.
	if _, err := ReadAntigravityToken(dir); err != nil {
		t.Errorf("read back: %v", err)
	}
}

// Writing something agy cannot use would leave a provider that looks signed in
// and is not.
func TestWriteAntigravityTokenRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAntigravityToken(dir, []byte(`{"token":{}}`)); err == nil {
		t.Error("a document without a token was stored")
	}
	if _, err := os.Stat(filepath.Join(dir, AntigravityAppDataDir)); !os.IsNotExist(err) {
		t.Error("the directory was created for a document that was rejected")
	}
}
