// Package quota queries a provider's balance or plan usage.
//
// Sources are auto-detected from the provider's endpoint host, so a plain
// DeepSeek, Kimi, Zhipu or MiniMax provider needs no extra configuration; a
// provider may name one with the `quota` field or point quota_cmd at a script
// that prints its own answer. Most sources are TOML plugins (plugins/*.toml,
// and the user's <config dir>/quota.d/*.toml); the rest are Go. Nothing here runs unless a
// balance is asked for, so `ak` never makes a network call on its own.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
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
	// NoSource marks a provider no balance source serves, which is a gap in
	// coverage rather than a failure.
	NoSource bool `json:"no_source,omitempty"`
}

// Spent reports whether the provider has nothing left to spend: a plan window
// used up, or a balance at or below zero. back is when it has allowance again,
// the latest reset among its spent windows, or nil when that is not known. A
// failed query is never spent: nothing is known of it.
func (q Quota) Spent() (spent bool, back *time.Time) {
	if q.Error != "" {
		return false, nil
	}
	if q.Balance != nil && *q.Balance <= 0 {
		spent = true
	}
	for _, w := range q.Windows {
		if w.Used < 100 {
			continue
		}
		spent = true
		if w.Resets == nil {
			// One spent window with no reset time makes the whole wait unknown.
			return true, nil
		}
		if back == nil || w.Resets.After(*back) {
			t := *w.Resets
			back = &t
		}
	}
	if !spent {
		return false, nil
	}
	if q.Balance != nil && *q.Balance <= 0 {
		// A balance does not reset on its own.
		return true, nil
	}
	return true, back
}

// NextReset is the soonest reset among windows with allowance left, or nil.
func (q Quota) NextReset() *time.Time {
	var out *time.Time
	for _, w := range q.Windows {
		if w.Used >= 100 || w.Resets == nil {
			continue
		}
		if out == nil || w.Resets.Before(*out) {
			t := *w.Resets
			out = &t
		}
	}
	return out
}

// ErrNoSource is returned when no source matches a provider's endpoint.
var ErrNoSource = errors.New("no balance source")

// Source is a balance API.
type Source interface {
	ID() string
	// Match reports whether the source serves that endpoint host.
	Match(host string) bool
	Fetch(ctx context.Context, p config.Provider, key string) (Quota, error)
}

// Sources lists the known source ids, user plugins first.
func Sources() []string {
	return ids(current().sources)
}

func ids(sources []Source) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.ID())
	}
	return out
}

// DedupKey identifies the account a query would ask about: providers sharing
// it get the same answer, so it need be asked once.
func DedupKey(p config.Provider, key string) string {
	vars := make([]string, 0, len(p.QuotaVars))
	for k, v := range p.QuotaVars {
		vars = append(vars, k+"="+v)
	}
	sort.Strings(vars)
	return strings.Join([]string{key, hostOf(p.BaseURL), p.Quota, p.QuotaCmd, strings.Join(vars, "&")}, "\x00")
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
		return Quota{Provider: name, Error: err.Error(), NoSource: errors.Is(err, ErrNoSource)}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	q, err := src.Fetch(ctx, p, key)
	q.Provider, q.Source = name, src.ID()
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// HasSource reports whether a balance query can be made for the provider: it
// has quota_cmd, or a source is named or detected and not turned off.
func HasSource(p config.Provider) bool {
	if p.QuotaCmd != "" {
		return true
	}
	_, err := resolve(p)
	return err == nil
}

// resolve picks the source for a provider: the named one, else the first whose
// host matches, unless the query is turned off.
func resolve(p config.Provider) (Source, error) {
	sources := current().sources
	switch p.Quota {
	case "off":
		return nil, fmt.Errorf("quota query is off for this provider")
	case "":
		host := hostOf(p.BaseURL)
		for _, s := range sources {
			if s.Match(host) {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%w for %s: set quota to %s, or quota_cmd",
			ErrNoSource, dash(host), strings.Join(ids(sources), "/"))
	default:
		for _, s := range sources {
			if s.ID() == p.Quota {
				return s, nil
			}
		}
		return nil, fmt.Errorf("unknown quota source %q; known: %s", p.Quota, strings.Join(ids(sources), ", "))
	}
}

// runScript runs the provider's quota_cmd. The key and base URL are passed in
// the environment so they never appear on the command line.
func runScript(ctx context.Context, p config.Provider, key string) (Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", p.QuotaCmd)
	cmd.Env = append(os.Environ(), "AK_QUOTA_KEY="+key, "AK_QUOTA_BASE_URL="+p.BaseURL)
	for k, v := range p.QuotaVars {
		cmd.Env = append(cmd.Env, "AK_QUOTA_VAR_"+strings.ToUpper(k)+"="+v)
	}
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
	h := http.Header{}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	return doJSON(ctx, http.MethodGet, rawURL, h, "", dst)
}

// doJSON sends one request and decodes a 200 reply into dst, numbers kept as
// json.Number.
func doJSON(ctx context.Context, method, rawURL string, h http.Header, body string, dst any) error {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return err
	}
	req.Header = h.Clone()
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	return dec.Decode(dst)
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
