// Package secrets resolves provider secrets. The first stage only supports
// plaintext; the interface leaves room for the env:/cmd:/keychain: reference
// forms so extending them later does not touch the callers.
package secrets

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// Resolver turns a configured secret declaration into plaintext.
type Resolver interface {
	Resolve(p config.Provider) (string, error)
}

// Default returns the default resolver.
func Default() Resolver { return plain{} }

type plain struct{}

// Resolve prefers the plaintext api_key; otherwise it dispatches on the
// api_key_ref prefix.
func (plain) Resolve(p config.Provider) (string, error) {
	if p.APIKey != "" {
		return p.APIKey, nil
	}
	ref := p.APIKeyRef
	if ref == "" {
		return "", nil
	}
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return "", fmt.Errorf("api_key_ref %q is missing a scheme, expected something like env:NAME", ref)
	}
	switch scheme {
	case "env":
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", rest)
		}
		return v, nil
	case "cmd":
		out, err := exec.Command("sh", "-c", rest).Output()
		if err != nil {
			return "", fmt.Errorf("run %q: %w", rest, err)
		}
		return strings.TrimSpace(string(out)), nil
	case "keychain":
		return resolveKeychain(rest)
	default:
		return "", fmt.Errorf("unsupported api_key_ref scheme %q", scheme)
	}
}

// resolveKeychain reads a secret from the macOS Keychain, in the form
// keychain:<service>/<account>.
func resolveKeychain(spec string) (string, error) {
	service, account, ok := strings.Cut(spec, "/")
	if !ok {
		return "", fmt.Errorf("keychain reference %q should look like keychain:service/account", spec)
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", service, "-a", account, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("read %s/%s from the Keychain: %w", service, account, err)
	}
	return strings.TrimSpace(string(out)), nil
}
