// Package secrets resolves provider secrets. The first stage only supports
// plaintext; the interface leaves room for the env:/cmd:/keychain: reference
// forms so extending them later does not touch the callers.
package secrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// cmdTimeout bounds a cmd: reference. A script that hangs would otherwise hang
// every request that resolves it. A variable so tests can shrink it.
var cmdTimeout = 10 * time.Second

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
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sh", "-c", rest).Output()
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

// Cache wraps a resolver and remembers what it returned, so a long-lived
// caller such as the pool gateway does not run a cmd: reference on every
// request. A success is kept for keyTTL, a failure for keyErrTTL.
type Cache struct {
	inner Resolver
	mu    sync.Mutex
	m     map[string]cachedKey
}

const (
	keyTTL    = 5 * time.Minute
	keyErrTTL = 30 * time.Second
)

type cachedKey struct {
	key string
	err error
	at  time.Time
}

// Cached wraps a resolver with a cache.
func Cached(inner Resolver) *Cache {
	return &Cache{inner: inner, m: map[string]cachedKey{}}
}

// Resolve returns the cached secret, or resolves and remembers it.
func (c *Cache) Resolve(p config.Provider) (string, error) {
	id := p.APIKey
	if id == "" {
		id = p.APIKeyRef
	}
	if id == "" {
		return "", nil
	}
	c.mu.Lock()
	if e, ok := c.m[id]; ok {
		ttl := keyTTL
		if e.err != nil {
			ttl = keyErrTTL
		}
		if time.Since(e.at) < ttl {
			c.mu.Unlock()
			return e.key, e.err
		}
	}
	c.mu.Unlock()

	key, err := c.inner.Resolve(p)
	c.mu.Lock()
	c.m[id] = cachedKey{key: key, err: err, at: time.Now()}
	c.mu.Unlock()
	return key, err
}
