package quota

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/pelletier/go-toml/v2"
)

// spec is a quota source declared in TOML: one request, and paths into its JSON
// reply. The embedded built-ins and the user's quota.d files share it.
type spec struct {
	ID      string       `toml:"id"`
	Match   []string     `toml:"match"` // endpoint host substrings
	Kind    string       `toml:"kind"`  // plan | balance; inferred when empty
	Detail  string       `toml:"detail"`
	OK      *checkSpec   `toml:"ok"`
	Request requestSpec  `toml:"request"`
	Windows []windowSpec `toml:"windows"`
	Balance *balanceSpec `toml:"balance"`
}

// requestSpec is the one request a plugin makes. URL, header values and body
// take {{key}}, {{base}}, {{root}}, {{var.NAME}} (quota_vars) and {{env.NAME}}; a|b falls back to b when a
// is empty. A header that expands to nothing is not sent.
type requestSpec struct {
	URL     string            `toml:"url"`
	Method  string            `toml:"method"`
	Headers map[string]string `toml:"headers"`
	Body    string            `toml:"body"`
}

// checkSpec fails a reply whose value at Path is present and differs from
// Equals, reporting the text at Message.
type checkSpec struct {
	Path    string `toml:"path"`
	Equals  any    `toml:"equals"`
	Message string `toml:"message"`
}

// windowSpec reads one plan window. With Each, it reads one window per item of
// that array whose fields match Where (a|b for alternatives), paths then being
// relative to the item. The share used comes from exactly one of Percent,
// PercentLeft, Used with Limit, or Remaining with Limit. A window whose values
// are missing is skipped.
type windowSpec struct {
	Name        string         `toml:"name"`
	Each        string         `toml:"each"`
	Where       map[string]any `toml:"where"`
	Percent     string         `toml:"percent"`
	PercentLeft string         `toml:"percent_left"`
	Used        string         `toml:"used"`
	Remaining   string         `toml:"remaining"`
	Limit       string         `toml:"limit"`
	Resets      string         `toml:"resets"` // RFC3339, unix seconds or millis
}

// balanceSpec reads money left. Every number read is divided by Scale.
type balanceSpec struct {
	Value        string  `toml:"value"`
	Used         string  `toml:"used"`
	Limit        string  `toml:"limit"`
	Currency     string  `toml:"currency"`
	CurrencyPath string  `toml:"currency_path"`
	Scale        float64 `toml:"scale"`
}

// plugin is a Source backed by a spec.
type plugin struct {
	spec spec
	file string // where it came from, for messages
}

func (pl *plugin) ID() string { return pl.spec.ID }

func (pl *plugin) Match(host string) bool {
	for _, m := range pl.spec.Match {
		if m != "" && strings.Contains(host, m) {
			return true
		}
	}
	return false
}

func (pl *plugin) Fetch(ctx context.Context, p config.Provider, key string) (Quota, error) {
	r := pl.spec.Request
	vars := func(name string) string { return requestVar(name, p, key) }
	header := http.Header{}
	for k, v := range r.Headers {
		if v = strings.TrimSpace(expand(v, vars)); v != "" {
			header.Set(k, v)
		}
	}
	method := strings.ToUpper(r.Method)
	if method == "" {
		method = http.MethodGet
	}
	var body any
	if err := doJSON(ctx, method, expand(r.URL, vars), header, expand(r.Body, vars), &body); err != nil {
		return Quota{}, err
	}
	return pl.spec.read(body)
}

// parsePlugin decodes and checks one plugin file; errors name the file.
func parsePlugin(file string, b []byte) (*plugin, error) {
	var s spec
	err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&s)
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		var keys []string
		for _, e := range strict.Errors {
			keys = append(keys, strings.Join(e.Key(), "."))
		}
		return nil, fmt.Errorf("%s: unknown keys %s", file, strings.Join(keys, ", "))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &plugin{spec: s, file: file}, nil
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func (s spec) validate() error {
	if !idRe.MatchString(s.ID) || s.ID == "off" {
		return fmt.Errorf("id %q must be lower-case letters, digits, '.', '_' or '-'", s.ID)
	}
	if s.Kind != "" && s.Kind != "plan" && s.Kind != "balance" {
		return fmt.Errorf("kind %q must be plan or balance", s.Kind)
	}
	if s.Request.URL == "" {
		return fmt.Errorf("request.url is required")
	}
	for _, t := range append([]string{s.Request.URL, s.Request.Body}, headerValues(s.Request.Headers)...) {
		if err := checkTemplate(t); err != nil {
			return err
		}
	}
	if s.OK != nil && s.OK.Path == "" {
		return fmt.Errorf("ok.path is required")
	}
	if len(s.Windows) == 0 && s.Balance == nil {
		return fmt.Errorf("declares neither [[windows]] nor [balance]")
	}
	for i, w := range s.Windows {
		if err := w.validate(); err != nil {
			return fmt.Errorf("windows[%d]: %w", i, err)
		}
	}
	if b := s.Balance; b != nil {
		switch {
		case b.Value == "":
			return fmt.Errorf("balance.value is required")
		case b.Currency != "" && b.CurrencyPath != "":
			return fmt.Errorf("balance takes currency or currency_path, not both")
		case b.Scale < 0:
			return fmt.Errorf("balance.scale must be positive")
		}
	}
	return nil
}

func (w windowSpec) validate() error {
	if w.Name == "" {
		return fmt.Errorf("name is required")
	}
	n := 0
	for _, v := range []string{w.Percent, w.PercentLeft, w.Used, w.Remaining} {
		if v != "" {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("set exactly one of percent, percent_left, used, remaining")
	}
	if (w.Used != "" || w.Remaining != "") && w.Limit == "" {
		return fmt.Errorf("used and remaining need limit")
	}
	if len(w.Where) > 0 && w.Each == "" {
		return fmt.Errorf("where needs each")
	}
	return nil
}

func headerValues(h map[string]string) []string {
	out := make([]string, 0, len(h))
	for _, v := range h {
		out = append(out, v)
	}
	return out
}

// read turns a decoded reply into a Quota.
func (s spec) read(body any) (Quota, error) {
	if c := s.OK; c != nil {
		if got, ok := lookup(body, c.Path); ok && text(got) != text(c.Equals) {
			msg := "the response reports a failure"
			if m, ok := lookup(body, c.Message); ok && c.Message != "" {
				msg = text(m)
			}
			return Quota{}, fmt.Errorf("%s", msg)
		}
	}
	q := Quota{Kind: s.Kind, Detail: strings.TrimSpace(expand(s.Detail, func(path string) string {
		v, _ := lookup(body, path)
		return text(v)
	}))}
	if q.Kind == "" {
		q.Kind = "plan"
		if s.Balance != nil {
			q.Kind = "balance"
		}
	}
	if b := s.Balance; b != nil {
		if err := b.read(body, &q); err != nil {
			return Quota{}, err
		}
	}
	for _, w := range s.Windows {
		q.Windows = append(q.Windows, w.read(body)...)
	}
	if len(s.Windows) > 0 && len(q.Windows) == 0 && q.Balance == nil {
		return Quota{}, fmt.Errorf("the response carried no usage windows")
	}
	return q, nil
}

func (b balanceSpec) read(body any, q *Quota) error {
	scale := b.Scale
	if scale == 0 {
		scale = 1
	}
	get := func(path string) *float64 {
		if path == "" {
			return nil
		}
		v, ok := lookupNum(body, path)
		if !ok {
			return nil
		}
		v /= scale
		return &v
	}
	if q.Balance = get(b.Value); q.Balance == nil {
		return fmt.Errorf("the response carried no balance at %s", b.Value)
	}
	q.Used, q.Limit = get(b.Used), get(b.Limit)
	q.Currency = b.Currency
	if b.CurrencyPath != "" {
		v, _ := lookup(body, b.CurrencyPath)
		q.Currency = text(v)
	}
	return nil
}

func (w windowSpec) read(body any) []Window {
	items := []any{body}
	if w.Each != "" {
		v, _ := lookup(body, w.Each)
		arr, _ := v.([]any)
		items = items[:0]
		for _, it := range arr {
			if matches(it, w.Where) {
				items = append(items, it)
			}
		}
	}
	var out []Window
	for _, it := range items {
		used, ok := w.share(it)
		if !ok {
			continue
		}
		win := Window{Name: w.Name, Used: used}
		if v, ok := lookup(it, w.Resets); ok && w.Resets != "" {
			win.Resets = parseTime(v)
		}
		out = append(out, win)
	}
	return out
}

// share is the percentage of the window used.
func (w windowSpec) share(item any) (float64, bool) {
	switch {
	case w.Percent != "":
		return lookupNum(item, w.Percent)
	case w.PercentLeft != "":
		v, ok := lookupNum(item, w.PercentLeft)
		return 100 - v, ok
	}
	limit, ok := lookupNum(item, w.Limit)
	if !ok {
		return 0, false
	}
	var used float64
	if w.Used != "" {
		used, ok = lookupNum(item, w.Used)
	} else {
		var left float64
		left, ok = lookupNum(item, w.Remaining)
		used = max(limit-left, 0)
	}
	if !ok {
		return 0, false
	}
	if limit <= 0 {
		return 0, true
	}
	return used / limit * 100, true
}

// matches reports whether every Where field of item equals one of its a|b
// alternatives, ignoring case.
func matches(item any, where map[string]any) bool {
	for k, want := range where {
		got, ok := lookup(item, k)
		if !ok {
			return false
		}
		hit := false
		for _, alt := range strings.Split(text(want), "|") {
			if strings.EqualFold(text(got), alt) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// parseTime reads an RFC3339 string or a unix time in seconds or millis; zero
// and negative times mean none.
func parseTime(v any) *time.Time {
	if s, ok := v.(string); ok {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil
			}
			return &t
		}
	}
	n, ok := toNum(v)
	if !ok || n <= 0 {
		return nil
	}
	var t time.Time
	if n < 1e12 {
		t = time.Unix(int64(n), 0)
	} else {
		t = time.UnixMilli(int64(n))
	}
	return &t
}

var tmplRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// expand replaces each {{a|b}} with the first alternative that resolves to a
// non-empty string.
func expand(s string, resolve func(string) string) string {
	return tmplRe.ReplaceAllStringFunc(s, func(m string) string {
		for _, alt := range strings.Split(tmplRe.FindStringSubmatch(m)[1], "|") {
			if v := resolve(strings.TrimSpace(alt)); v != "" {
				return v
			}
		}
		return ""
	})
}

// requestVar resolves one request template name.
func requestVar(name string, p config.Provider, key string) string {
	switch name {
	case "key":
		return key
	case "base":
		return strings.TrimRight(p.BaseURL, "/")
	case "root":
		return apiRoot(p.BaseURL)
	}
	if v, ok := strings.CutPrefix(name, "var."); ok {
		return p.QuotaVars[v]
	}
	if env, ok := strings.CutPrefix(name, "env."); ok {
		return p.Env[env]
	}
	return ""
}

// checkTemplate refuses a request template naming an unknown variable.
func checkTemplate(s string) error {
	for _, m := range tmplRe.FindAllStringSubmatch(s, -1) {
		for _, alt := range strings.Split(m[1], "|") {
			alt = strings.TrimSpace(alt)
			if alt == "key" || alt == "base" || alt == "root" ||
				(strings.HasPrefix(alt, "var.") && len(alt) > len("var.")) ||
				(strings.HasPrefix(alt, "env.") && len(alt) > len("env.")) {
				continue
			}
			return fmt.Errorf("unknown template {{%s}}; use key, base, root, var.NAME or env.NAME", alt)
		}
	}
	return nil
}
