package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// compatSuffixes are Anthropic-compatible sub-paths vendors hang off their
// root, longest first so /api/anthropic wins over /anthropic. A base URL
// ending in one is also tried with it stripped. Copied from cc-switch's
// KNOWN_COMPAT_SUFFIXES.
var compatSuffixes = []string{
	"/api/claudecode",
	"/api/anthropic",
	"/apps/anthropic",
	"/api/coding",
	"/claudecode",
	"/anthropic",
	"/step_plan",
	"/coding",
	"/claude",
}

// ModelsURLs lists the model-list endpoints to try for a base URL, in order
// and without repeats.
func ModelsURLs(base string) []string {
	base = trimBase(base)
	if base == "" {
		return nil
	}
	var out []string
	if endsWithVersion(base) {
		out = append(out, base+"/models")
		if !strings.HasSuffix(base, "/v1") {
			out = append(out, base+"/v1/models")
		}
	} else {
		out = append(out, base+"/v1/models", base+"/models")
	}
	for _, s := range compatSuffixes {
		if root, ok := strings.CutSuffix(base, s); ok {
			if strings.Contains(root, "://") && !strings.HasSuffix(root, "://") {
				out = append(out, root+"/v1/models", root+"/models")
			}
			break
		}
	}
	return dedupe(out)
}

// Models asks the provider for its model list, trying each candidate URL until
// one answers with a list. The ids come back sorted and without repeats.
func Models(ctx context.Context, p config.Provider, key string) ([]string, error) {
	if usesPiOwn(p) {
		return nil, errors.New("it uses pi's own provider; ask pi for its models")
	}
	if p.IsPool() {
		return nil, errors.New("a pool has no models of its own; name a member")
	}
	w, err := wire(p)
	if err != nil {
		return nil, err
	}
	urls := ModelsURLs(p.BaseURL)
	if len(urls) == 0 {
		return nil, errors.New("no base_url")
	}
	var tried []string
	for _, u := range urls {
		ids, err := fetchModels(ctx, p, w, u, key)
		if err == nil {
			return ids, nil
		}
		tried = append(tried, fmt.Sprintf("  %s: %s", redact(u, key), redact(err.Error(), key)))
	}
	return nil, fmt.Errorf("no model list found, tried:\n%s", strings.Join(tried, "\n"))
}

func fetchModels(ctx context.Context, p config.Provider, w, url, key string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	setHeaders(req.Header, p, w, key)
	code, body, err := do(ctx, req, 4<<20)
	if err != nil {
		return nil, err
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", code, snippet(body, key))
	}
	return parseModels(body)
}

// parseModels reads the OpenAI and Anthropic shape, data[].id, or a models[]
// list of ids or of objects with an id or name.
func parseModels(body []byte) ([]string, error) {
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, errors.New("not a JSON model list")
	}
	var ids []string
	for _, d := range v.Data {
		ids = append(ids, d.ID)
	}
	for _, raw := range v.Models {
		ids = append(ids, modelID(raw))
	}
	ids = dedupe(ids)
	if len(ids) == 0 {
		return nil, errors.New("no models in the reply")
	}
	sort.Strings(ids)
	return ids, nil
}

// modelID reads one models[] entry: a bare id, or an object's id, else its
// name without Gemini's models/ prefix.
func modelID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	if o.ID != "" {
		return o.ID
	}
	return strings.TrimPrefix(o.Name, "models/")
}

// dedupe drops empty and repeated strings, keeping first-seen order.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
