package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// AntigravityAppDataDir is the directory under gemini_dir where agy keeps
	// its own files, the credential among them. agy names it after the product;
	// --app_data_dir moves it, which ak does not expose.
	AntigravityAppDataDir = "antigravity-cli"

	// AntigravityTokenFile is the credential agy falls back to when the OS
	// keyring is not, or must not be, used. ak makes sure it is: the launch
	// exports SSH_CONNECTION, so this file is the only credential a launch
	// reads, and it lives inside the provider's own gemini_dir.
	AntigravityTokenFile = "antigravity-oauth-token"

	// keyringPrefix is how go-keyring, the library agy uses, wraps a value it
	// stores in the macOS Keychain. A value read back from there has to lose it
	// before it is the JSON document agy reads from a file.
	keyringPrefix = "go-keyring-base64:"
)

// AntigravityAppDataPath is the provider's agy product directory.
func AntigravityAppDataPath(geminiDir string) string {
	return filepath.Join(geminiDir, AntigravityAppDataDir)
}

// AntigravityTokenPath is the file agy reads the provider's credential from.
func AntigravityTokenPath(geminiDir string) string {
	return filepath.Join(AntigravityAppDataPath(geminiDir), AntigravityTokenFile)
}

// AntigravityConfigPath is the provider's shared config: hooks.json, MCP
// servers, skills and plugins.
func AntigravityConfigPath(geminiDir string) string {
	return filepath.Join(geminiDir, "config")
}

// AntigravityToken is what ak needs to know about a credential document. agy
// keeps more in it (a project, a region, a tier), which ak passes through
// untouched when it copies one.
type AntigravityToken struct {
	AccessToken  string
	RefreshToken string
	// Expiry is zero when the document says nothing, or something ak cannot
	// read; an expired access token is normal, since agy refreshes it itself.
	Expiry     time.Time
	AuthMethod string
	// Email is who signed in, from the ID token the document carries.
	Email string
}

// Usable reports whether the document can authenticate at all.
func (t AntigravityToken) Usable() bool {
	return t.AccessToken != "" || t.RefreshToken != ""
}

// Expired reports whether the access token is past its expiry. It is not an
// error: the refresh token is what keeps a provider signed in.
func (t AntigravityToken) Expired(now time.Time) bool {
	return !t.Expiry.IsZero() && now.After(t.Expiry)
}

// antigravityExpiring is how close to expiry ak warns about, when nothing can
// renew the token.
const antigravityExpiring = 7 * 24 * time.Hour

// State says what the expiry means for the account: "ok" while agy can renew
// the token itself or it is still fresh, "expiring" within a week, "expired"
// past it. A provider with a refresh token is ok whatever the access token
// says, since agy renews it on the next launch without asking anyone.
func (t AntigravityToken) State(now time.Time) string {
	if t.RefreshToken != "" || t.Expiry.IsZero() || !t.Expired(now.Add(antigravityExpiring)) {
		return "ok"
	}
	if t.Expired(now) {
		return "expired"
	}
	return "expiring"
}

// antigravityTokenDoc is the document agy stores, as far as ak reads it.
type antigravityTokenDoc struct {
	Token struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
	} `json:"token"`
	// The desktop and standalone builds write the expiry beside the token.
	Expiry       string `json:"expiry"`
	AuthMethod   string `json:"auth_method"`
	IDToken      string `json:"id_token"`
	IDTokenEmail string `json:"id_token_email"`
}

// ParseAntigravityToken reads a credential document. It accepts the plain JSON
// agy writes to a file and the base64 wrapper it stores in the Keychain.
func ParseAntigravityToken(raw []byte) (AntigravityToken, error) {
	body := UnwrapAntigravityCredential(raw)
	var doc antigravityTokenDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return AntigravityToken{}, fmt.Errorf("invalid credential JSON: %w", err)
	}
	t := AntigravityToken{
		AccessToken:  doc.Token.AccessToken,
		RefreshToken: doc.Token.RefreshToken,
		AuthMethod:   doc.AuthMethod,
		Email:        doc.IDTokenEmail,
		Expiry:       parseAntigravityExpiry(doc.Token.Expiry),
	}
	if t.Expiry.IsZero() {
		t.Expiry = parseAntigravityExpiry(doc.Expiry)
	}
	if t.Email == "" {
		t.Email = emailFromIDToken(doc.IDToken)
	}
	if !t.Usable() {
		return AntigravityToken{}, fmt.Errorf("credential has neither an access nor a refresh token")
	}
	return t, nil
}

// UnwrapAntigravityCredential strips go-keyring's wrapper, if the value has
// one, and returns the document underneath.
func UnwrapAntigravityCredential(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(s, keyringPrefix) {
		return []byte(s)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(s, keyringPrefix)))
	if err != nil {
		return []byte(s)
	}
	return decoded
}

// ReadAntigravityToken reads a provider's credential.
func ReadAntigravityToken(geminiDir string) (AntigravityToken, error) {
	path := AntigravityTokenPath(geminiDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return AntigravityToken{}, err
	}
	t, err := ParseAntigravityToken(raw)
	if err != nil {
		return AntigravityToken{}, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// WriteAntigravityToken stores a credential document where agy reads it, 0600,
// replacing whatever was there in one step.
func WriteAntigravityToken(geminiDir string, raw []byte) error {
	body := UnwrapAntigravityCredential(raw)
	if _, err := ParseAntigravityToken(body); err != nil {
		return err
	}
	path := AntigravityTokenPath(geminiDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// parseAntigravityExpiry reads the expiry as agy writes it, with or without
// fractional seconds, in UTC or with an offset.
func parseAntigravityExpiry(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// emailFromIDToken reads the address out of an OIDC ID token. The signature is
// not verified: the token was issued to agy's own client and is read back from
// the local credential store, so ak only wants the claim it already carries.
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Email
}
