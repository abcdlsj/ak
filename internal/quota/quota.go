// Package quota queries a provider's balance or plan usage.
//
// Sources are auto-detected from the provider's endpoint host, so a plain
// DeepSeek, OpenRouter, Moonshot or SiliconFlow provider needs no extra
// configuration; a provider may name one with the `quota` field or point
// quota_cmd at a script that prints its own answer. Nothing here runs unless a
// balance is asked for, so `ak` never makes a network call on its own.
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// Window is one allowance window of a plan, e.g. a five-hour or weekly quota.
type Window struct {
	Name   string     `json:"name"`
	Used   float64    `json:"used"` // share used, 0-100
	Resets *time.Time `json:"resets,omitempty"`
}

// Quota is one provider's balance or plan usage. Numbers that the source did
// not report are nil rather than zero.
type Quota struct {
	Provider string   `json:"provider"`
	Source   string   `json:"source"`
	Kind     string   `json:"kind,omitempty"` // balance | plan | custom
	Currency string   `json:"currency,omitempty"`
	Balance  *float64 `json:"balance,omitempty"` // money left
	Used     *float64 `json:"used,omitempty"`
	Limit    *float64 `json:"limit,omitempty"`
	Windows  []Window `json:"windows,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Source is a built-in balance API.
type Source interface {
	ID() string
	// Match reports whether the source serves that endpoint host.
	Match(host string) bool
	Fetch(ctx context.Context, base, key string) (Quota, error)
}

// Sources lists the built-in source ids, sorted by declaration order.
func Sources() []string {
	out := make([]string, 0, len(builtins))
	for _, s := range builtins {
		out = append(out, s.ID())
	}
	return out
}

// Query resolves the provider's source and asks it. A provider with quota_cmd
// uses that instead; the returned Quota always has Provider set, and any
// failure is in Error rather than returned, so the caller can print one row per
// provider.
func Query(ctx context.Context, name string, p config.Provider, key string) Quota {
	if p.QuotaCmd != "" {
		q, err := runScript(ctx, p, key)
		q.Provider = name
		if err != nil {
			q.Error = err.Error()
		}
		if q.Source == "" {
			q.Source = "script"
		}
		return q
	}
	src, err := resolve(p)
	if err != nil {
		return Quota{Provider: name, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	q, err := src.Fetch(ctx, p.BaseURL, key)
	q.Provider, q.Source = name, src.ID()
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// resolve picks the source for a provider: the named one, else the first whose
// host matches, unless the query is turned off.
func resolve(p config.Provider) (Source, error) {
	switch p.Quota {
	case "off":
		return nil, fmt.Errorf("quota query is off for this provider")
	case "":
		host := hostOf(p.BaseURL)
		for _, s := range builtins {
			if s.Match(host) {
				return s, nil
			}
		}
		return nil, fmt.Errorf("no built-in balance source for %s: set quota to %s, or quota_cmd",
			dash(host), strings.Join(Sources(), "/"))
	default:
		for _, s := range builtins {
			if s.ID() == p.Quota {
				return s, nil
			}
		}
		return nil, fmt.Errorf("unknown quota source %q; known: %s", p.Quota, strings.Join(Sources(), ", "))
	}
}

// runScript runs the provider's quota_cmd. The key and base URL are passed in
// the environment so they never appear on the command line.
func runScript(ctx context.Context, p config.Provider, key string) (Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", p.QuotaCmd)
	cmd.Env = append(os.Environ(), "AK_QUOTA_KEY="+key, "AK_QUOTA_BASE_URL="+p.BaseURL)
	out, err := cmd.Output()
	if err != nil {
		return Quota{}, fmt.Errorf("quota_cmd: %w", err)
	}
	return parseScript(out)
}

// parseScript reads a script's output: a JSON Quota, or a bare number taken as
// a balance.
func parseScript(b []byte) (Quota, error) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return Quota{}, fmt.Errorf("quota_cmd printed nothing")
	}
	if strings.HasPrefix(s, "{") {
		var q Quota
		if err := json.Unmarshal([]byte(s), &q); err != nil {
			return Quota{}, err
		}
		if q.Kind == "" {
			q.Kind = "custom"
		}
		return q, nil
	}
	var f flexFloat
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return Quota{}, fmt.Errorf("quota_cmd must print a JSON object or a number")
	}
	v := float64(f)
	return Quota{Kind: "balance", Balance: &v}, nil
}

// client is shared by every source; the per-query context bounds the wait.
var client = &http.Client{Timeout: 15 * time.Second}

// getJSON GETs a URL with the key as a bearer token and decodes the reply.
func getJSON(ctx context.Context, rawURL, key string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// apiRoot is the scheme and host of a base URL, with any path dropped, so a
// balance endpoint can be appended regardless of how the provider's API path is
// spelled (/v1, /anthropic, …).
func apiRoot(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return strings.TrimRight(base, "/")
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return strings.TrimRight(u.String(), "/")
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Host
	}
	return ""
}

// flexFloat accepts a number or a numeric string, as these APIs are not
// consistent about which they use.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

func dash(s string) string {
	if s == "" {
		return "the endpoint"
	}
	return s
}
