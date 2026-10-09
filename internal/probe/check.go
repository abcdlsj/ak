package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// SlowAfter is the latency over which a successful check is reported slow.
var SlowAfter = 6 * time.Second

// Status is the outcome of one check.
type Status string

const (
	StatusOK    Status = "ok"
	StatusSlow  Status = "slow"  // answered, but slower than SlowAfter
	StatusAuth  Status = "auth"  // 401 or 403: the key was rejected
	StatusModel Status = "model" // the upstream does not serve the model
	StatusFail  Status = "fail"  // any other status, or no answer at all
	StatusSkip  Status = "skip"  // nothing ak can ask
)

// Passed reports whether the status counts as a working provider. A skipped
// check is not a failure.
func (s Status) Passed() bool {
	return s == StatusOK || s == StatusSlow || s == StatusSkip
}

// Result is one provider's check.
type Result struct {
	Provider  string   `json:"provider"`
	Status    Status   `json:"status"`
	Model     string   `json:"model,omitempty"`
	Code      int      `json:"code,omitempty"`
	LatencyMS int64    `json:"latency_ms,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Members   []string `json:"members,omitempty"`
}

// modelWords spot an error that names the model rather than the request.
var modelWords = regexp.MustCompile(`(?i)model_not_found|no such model|unknown model|invalid model|unsupported model|model.{0,60}(not found|does not exist|doesn't exist|not exist|not supported|not available|unavailable|not allowed)`)

// Check sends the provider one minimal, non-streamed request and classifies
// the answer. It spends a few tokens. Provider is left for the caller to set.
func Check(ctx context.Context, p config.Provider, key string) Result {
	if usesPiOwn(p) {
		return Result{Status: StatusSkip, Detail: "skipped (pi's own provider)"}
	}
	if p.IsPool() {
		return Result{Status: StatusSkip, Detail: "a pool is checked through its members"}
	}
	if trimBase(p.BaseURL) == "" {
		return Result{Status: StatusFail, Detail: "no base_url"}
	}
	model := testModel(p)
	if model == "" {
		return Result{Status: StatusSkip, Detail: "no model to test"}
	}
	w, err := wire(p)
	if err != nil {
		return Result{Status: StatusFail, Model: model, Detail: err.Error()}
	}
	url, body := checkRequest(p.BaseURL, w, model)
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return Result{Status: StatusFail, Model: model, Detail: redact(err.Error(), key)}
	}
	req.Header.Set("Content-Type", "application/json")
	setHeaders(req.Header, p, w, key)

	start := time.Now()
	code, reply, err := do(ctx, req, 64<<10)
	elapsed := time.Since(start)
	r := Result{Model: model, Code: code, LatencyMS: elapsed.Milliseconds()}
	if err != nil {
		r.Status, r.Detail = StatusFail, redact(err.Error(), key)
		return r
	}
	r.Status, r.Detail = classify(code, reply, elapsed, key)
	return r
}

// testModel is the model a check asks for: the provider's model, else, for
// claude, its sonnet tier.
func testModel(p config.Provider) string {
	if p.Model != "" {
		return p.Model
	}
	if p.Kind == config.KindClaude {
		return p.Sonnet
	}
	return ""
}

// checkRequest builds the smallest request each protocol accepts. A codex or
// openai-style base URL conventionally already ends in /v1; an Anthropic one
// does not.
func checkRequest(base, w, model string) (string, any) {
	base = trimBase(base)
	hi := []map[string]string{{"role": "user", "content": "hi"}}
	switch w {
	case wireChat:
		return base + "/chat/completions", map[string]any{"model": model, "messages": hi, "max_tokens": 1}
	case wireResponses:
		return base + "/responses", map[string]any{"model": model, "input": "hi", "max_output_tokens": 16}
	}
	if endsWithVersion(base) {
		return base + "/messages", map[string]any{"model": model, "max_tokens": 1, "messages": hi}
	}
	return base + "/v1/messages", map[string]any{"model": model, "max_tokens": 1, "messages": hi}
}

// classify maps a status code and body onto a Status and a short detail.
func classify(code int, body []byte, elapsed time.Duration, key string) (Status, string) {
	switch {
	case code >= 200 && code < 300:
		if elapsed > SlowAfter {
			return StatusSlow, fmt.Sprintf("slower than %s", SlowAfter)
		}
		return StatusOK, ""
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return StatusAuth, fmt.Sprintf("key rejected (HTTP %d): %s", code, snippet(body, key))
	case (code == http.StatusNotFound || code == http.StatusBadRequest) && modelWords.Match(body):
		return StatusModel, fmt.Sprintf("model not served (HTTP %d): %s", code, snippet(body, key))
	}
	return StatusFail, fmt.Sprintf("HTTP %d: %s", code, snippet(body, key))
}

// Aggregate folds a pool's member results into the pool's own row: ok when
// any member passes, with the fastest passing member's latency.
func Aggregate(name string, members []Result) Result {
	r := Result{Provider: name, Status: StatusFail}
	var best *Result
	passed, skipped := 0, 0
	for i, m := range members {
		r.Members = append(r.Members, m.Provider)
		switch m.Status {
		case StatusOK, StatusSlow:
			passed++
			if best == nil || better(m, *best) {
				best = &members[i]
			}
		case StatusSkip:
			skipped++
		}
	}
	switch {
	case best != nil:
		r.Status, r.LatencyMS = best.Status, best.LatencyMS
	case len(members) > 0 && skipped == len(members):
		r.Status = StatusSkip
	}
	r.Detail = fmt.Sprintf("%d/%d members ok", passed, len(members))
	return r
}

// better prefers an ok member to a slow one, then the faster.
func better(a, b Result) bool {
	if a.Status != b.Status {
		return a.Status == StatusOK
	}
	return a.LatencyMS < b.LatencyMS
}
