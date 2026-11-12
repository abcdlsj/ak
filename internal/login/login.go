// Package login obtains an account's credentials by one of its vendor's
// login methods: reading a file another tool signed in with, or running a
// device authorization. Pasting a key needs no help and is the caller's.
package login

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/vendor"
)

// Credentials are what a sign-in hands out.
type Credentials struct {
	Key string
	// Secrets are further credentials, by the name the balance query reads.
	Secrets map[string]string
	// User names the account, when the vendor says.
	User string
}

// FilePath is the file a file login reads: the first of its paths that
// exists, or "" when none does.
func FilePath(l vendor.Login) string {
	for _, p := range l.Paths {
		if p = expand(p); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// expand resolves ~ and $VAR in a path; one naming an unset variable is "".
func expand(p string) string {
	unset := false
	p = os.Expand(p, func(name string) string {
		v := os.Getenv(name)
		unset = unset || v == ""
		return v
	})
	if unset {
		return ""
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		p = filepath.Join(home, rest)
	}
	return p
}

// tilde shortens a path under the home directory to ~/….
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return p
}

// File reads the key a file login names. The file is only read.
func File(l vendor.Login) (Credentials, error) {
	path := FilePath(l)
	if path == "" {
		return Credentials{}, fmt.Errorf("not signed in there: none of %s exists", strings.Join(l.Paths, ", "))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return Credentials{}, fmt.Errorf("%s: %w", path, err)
	}
	key := text(lookup(doc, l.Key))
	if key == "" {
		return Credentials{}, fmt.Errorf("%s holds no key at %s", path, l.Key)
	}
	return Credentials{Key: key}, nil
}

// Prompt is what the user must do to approve a device sign-in.
type Prompt struct {
	Code string
	URL  string
}

// Device runs a device authorization: it starts one, hands the code and
// page to show to prompt, and polls until the page approves it, the code
// runs out or ctx ends.
func Device(ctx context.Context, d vendor.Device, prompt func(Prompt)) (Credentials, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return Credentials{}, fmt.Errorf("this computer's name cannot be read, and the sign-in sends it")
	}
	vars := map[string]string{"host": host}
	_, start, err := post(ctx, d.Start, vars)
	if err != nil {
		return Credentials{}, fmt.Errorf("start the sign-in: %w", err)
	}
	if m, ok := start.(map[string]any); ok {
		for k, v := range m {
			if s := text(v); s != "" {
				vars[k] = s
			}
		}
	}
	p := Prompt{Code: text(lookup(start, d.Code)), URL: text(lookup(start, d.VerifyURL))}
	if p.URL == "" {
		return Credentials{}, fmt.Errorf("the vendor sent no page to approve the sign-in on")
	}
	interval := 2 * time.Second
	if n, err := strconv.ParseFloat(text(lookup(start, d.Interval)), 64); err == nil && n > 0 {
		interval = time.Duration(n * float64(time.Second))
	}
	deadline := time.Now().Add(10 * time.Minute)
	if t, err := time.Parse(time.RFC3339, text(lookup(start, d.Expires))); err == nil {
		deadline = t
	}
	prompt(p)

	for {
		select {
		case <-ctx.Done():
			return Credentials{}, ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return Credentials{}, errors.New("the code ran out before it was approved; start again")
		}
		status, reply, err := post(ctx, d.Poll, vars)
		if err != nil && status == 0 {
			continue // a hiccup on the way is not the sign-in failing
		}
		if key := text(lookup(reply, d.Key)); key != "" {
			c := Credentials{Key: key, User: text(lookup(reply, d.User))}
			for name, path := range d.Secrets {
				if s := text(lookup(reply, path)); s != "" {
					if c.Secrets == nil {
						c.Secrets = map[string]string{}
					}
					c.Secrets[name] = s
				}
			}
			return c, nil
		}
		if refused(status, reply) {
			return Credentials{}, fmt.Errorf("the sign-in was refused (%s); start again", said(status, reply))
		}
	}
}

// refused reports a poll reply that will never turn into an approval.
func refused(status int, reply any) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return true
	}
	switch strings.ToLower(text(lookup(reply, "status"))) {
	case "expired", "denied", "rejected", "canceled", "cancelled":
		return true
	}
	return false
}

func said(status int, reply any) string {
	for _, p := range []string{"error.message", "message", "error", "status"} {
		if s := text(lookup(reply, p)); s != "" {
			return s
		}
	}
	return http.StatusText(status)
}

// post sends a JSON POST with its {{vars}} expanded and decodes the reply,
// whatever its status: a pending poll answers with a 4xx in some APIs. The
// status is 0 when no reply came.
func post(ctx context.Context, r vendor.Request, vars map[string]string) (int, any, error) {
	body, err := json.Marshal(fill(r.Body, vars))
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fillText(r.URL, vars), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var reply any
	_ = json.Unmarshal(b, &reply)
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, reply, fmt.Errorf("%s", resp.Status)
	}
	return resp.StatusCode, reply, nil
}

var client = &http.Client{Timeout: 20 * time.Second}

var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// fillText replaces each {{NAME}} with its value; inside a URL the value is
// path-escaped.
func fillText(s string, vars map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		return url.PathEscape(vars[varRe.FindStringSubmatch(m)[1]])
	})
}

// fill expands {{NAME}} in a request body's string values.
func fill(body map[string]any, vars map[string]string) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if s, ok := v.(string); ok {
			v = varRe.ReplaceAllStringFunc(s, func(m string) string { return vars[varRe.FindStringSubmatch(m)[1]] })
		}
		out[k] = v
	}
	return out
}

// lookup follows a dotted path through decoded JSON.
func lookup(v any, path string) any {
	if path == "" {
		return nil
	}
	for _, seg := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[seg]
	}
	return v
}

// text is a JSON scalar as a string, "" for anything else.
func text(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}
