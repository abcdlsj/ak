// Package probe talks to a provider's upstream directly: a minimal real
// request to see whether it answers, and its model list. Nothing here goes
// through the pool gateway, and nothing runs unless a command asks.
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// Timeout bounds one request. A variable so tests can shrink it.
var Timeout = 20 * time.Second

// anthropicVersion is the Messages API version every Anthropic call names.
const anthropicVersion = "2023-06-01"

var client = &http.Client{}

// Wire protocols a provider speaks.
const (
	wireAnthropic = "anthropic"
	wireChat      = "chat"
	wireResponses = "responses"
)

// wire returns the protocol the provider's engine speaks upstream.
func wire(p config.Provider) (string, error) {
	switch p.Kind {
	case config.KindClaude:
		return wireAnthropic, nil
	case config.KindCodex:
		if p.WireAPI == "chat" {
			return wireChat, nil
		}
		return wireResponses, nil
	case config.KindPi:
		switch p.PiAPI {
		case "", "anthropic-messages":
			return wireAnthropic, nil
		case "openai-completions":
			return wireChat, nil
		case "openai-responses":
			return wireResponses, nil
		}
		return "", fmt.Errorf("pi_api %q is not supported", p.PiAPI)
	}
	return "", fmt.Errorf("kind %q is not supported", p.Kind)
}

// usesPiOwn reports whether a pi provider names one of pi's own providers, so
// ak has no upstream of its own to ask.
func usesPiOwn(p config.Provider) bool {
	return p.Kind == config.KindPi && p.PiProvider != ""
}

// setHeaders writes the key the way the provider's engine sends it. It mirrors
// the gateway's setAuth: x-api-key for claude with key_field api_key and for
// pi on the Anthropic API without pi_auth_header, a bearer token otherwise.
func setHeaders(h http.Header, p config.Provider, w, key string) {
	if w == wireAnthropic {
		h.Set("anthropic-version", anthropicVersion)
	}
	if key == "" {
		return
	}
	if usesAPIKeyHeader(p) {
		h.Set("X-Api-Key", key)
		return
	}
	h.Set("Authorization", "Bearer "+key)
}

func usesAPIKeyHeader(p config.Provider) bool {
	switch p.Kind {
	case config.KindClaude:
		return p.KeyField == "api_key"
	case config.KindPi:
		return (p.PiAPI == "" || p.PiAPI == "anthropic-messages") && !p.PiAuthHeader
	}
	return false
}

// trimBase drops surrounding space and trailing slashes from a base URL.
func trimBase(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

// endsWithVersion reports whether the URL's last segment is a version, /v1 or
// /v4, so the version is already in the path.
func endsWithVersion(u string) bool {
	last := u[strings.LastIndex(u, "/")+1:]
	digits, ok := strings.CutPrefix(last, "v")
	if !ok || digits == "" {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// do sends one request under Timeout and reads at most limit bytes of the
// reply.
func do(ctx context.Context, req *http.Request, limit int64) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, nil, fmt.Errorf("timed out after %s", Timeout)
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil && len(body) == 0 {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// redact replaces every occurrence of the key with ***.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "***")
}

// snippet is a body cut to one short line, the key redacted.
func snippet(b []byte, key string) string {
	s := strings.Join(strings.Fields(redact(string(b), key)), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}
