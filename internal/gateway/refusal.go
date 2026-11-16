package gateway

// A vendor that answers "this model is not on your plan" or "no such model"
// says the same thing to every request after it. Asking again costs a round
// trip and, on a pool, makes a member that can never serve it look like a
// candidate: remember the answer instead, and answer at once until it may
// have changed.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// refusalTTL is how long a refusal is remembered. A plan that excluded a
// model changes with a purchase, and a rejected key with a new one, so
// neither is kept forever.
const refusalTTL = 24 * time.Hour

// Reasons a member refuses a model.
const (
	// RefusalPermission is a plan that does not include the model.
	RefusalPermission = "permission_error"
	// RefusalMissing is a model the account does not have.
	RefusalMissing = "model_not_found"
	// RefusalKey is a key the vendor will not take.
	RefusalKey = "invalid_key"
	// RefusalProtocol is a protocol the account cannot serve this model on,
	// by the configuration (a member that could only be translated) or by the
	// vendor's own words ("not supported on this endpoint").
	RefusalProtocol = "unsupported_protocol"
)

// refusal is one answer a vendor gave about one model.
type refusal struct {
	Reason string    `json:"reason"`
	Status int       `json:"status,omitempty"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// refusals remembers what each account says it cannot serve, by account,
// model and protocol.
type refusals struct {
	mu    sync.Mutex
	m     map[string]refusal
	path  string
	dirty bool
}

func newRefusals() *refusals { return &refusals{m: map[string]refusal{}} }

// refusalKey names one account's model on one protocol.
func refusalKey(account, model, protocol string) string {
	return account + "\x00" + model + "\x00" + protocol
}

// get is what was said of this account's model on this protocol, if it was
// said and still holds.
func (r *refusals) get(account, model, protocol string) (refusal, bool) {
	if r == nil || account == "" || model == "" {
		return refusal{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	x, ok := r.m[refusalKey(account, model, protocol)]
	if !ok || time.Since(x.At) > refusalTTL {
		return refusal{}, false
	}
	return x, true
}

// note records a refusal, replacing what an earlier request learned.
func (r *refusals) note(account, model, protocol, reason string, status int, detail string) {
	if r == nil || account == "" || model == "" || reason == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := refusalKey(account, model, protocol)
	if x, ok := r.m[k]; ok && x.Reason == reason {
		return
	}
	r.m[k] = refusal{Reason: reason, Status: status, Detail: detail, At: time.Now()}
	r.dirty = true
}

// splitRefusalKey unpacks "account\x00model\x00protocol".
func splitRefusalKey(k string) (account, model, protocol string, ok bool) {
	parts := strings.Split(k, "\x00")
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// of lists what is refused for an account, for a model name to report.
func (r *refusals) of(account string) []RefusalRow {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RefusalRow
	for k, x := range r.m {
		if time.Since(x.At) > refusalTTL {
			continue
		}
		acct, model, protocol, ok := splitRefusalKey(k)
		if !ok || acct != account {
			continue
		}
		out = append(out, RefusalRow{Model: model, Protocol: protocol, Reason: x.Reason, At: x.At})
	}
	return out
}

// forgetAll drops what was learned, so a bought plan or a new key takes
// effect without waiting.
func (r *refusals) forgetAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.m) == 0 {
		return
	}
	r.m = map[string]refusal{}
	r.dirty = true
}

// RefusalRow is one model an account is known to refuse.
type RefusalRow struct {
	Model    string    `json:"model"`
	Protocol string    `json:"protocol"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

// savedRefusal is one refusal as the file holds it.
type savedRefusal struct {
	Account  string  `json:"account"`
	Model    string  `json:"model"`
	Protocol string  `json:"protocol"`
	Refusal  refusal `json:"refusal"`
}

// load reads the refusals left by an earlier run, so a restart does not ask
// again what was already refused.
func (r *refusals) load(path string) {
	if r == nil || path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved []savedRefusal
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range saved {
		if time.Since(x.Refusal.At) > refusalTTL {
			continue
		}
		r.m[refusalKey(x.Account, x.Model, x.Protocol)] = x.Refusal
	}
}

// save writes them when something changed.
func (r *refusals) save(path string) {
	if r == nil || path == "" {
		return
	}
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	saved := make([]savedRefusal, 0, len(r.m))
	for k, x := range r.m {
		if time.Since(x.At) > refusalTTL {
			continue
		}
		account, model, protocol, ok := splitRefusalKey(k)
		if !ok {
			continue
		}
		saved = append(saved, savedRefusal{Account: account, Model: model, Protocol: protocol, Refusal: x})
	}
	r.dirty = false
	r.mu.Unlock()

	data, err := json.Marshal(saved)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if config.AtomicWrite(path, data, 0o600) != nil {
		return
	}
}

// refusalOf classifies an upstream's answer: a status and body that say the
// member will not serve this model, rather than that the request is wrong.
var (
	permissionWords = regexp.MustCompile(`(?i)permission_error|unapproved|not allowed|no access|forbidden|permission`)
	missingWords    = regexp.MustCompile(`(?i)model_not_found|no such model|model .* does not exist|unknown model|invalid model|does not have access to model`)
	// unsupportedWords is a vendor saying this endpoint does not speak this
	// model: the account has it, but on another protocol, so the answer is
	// worth remembering as a protocol capability rather than retried.
	unsupportedWords = regexp.MustCompile(`(?i)not supported on this endpoint|does not support this (model|endpoint)|unsupported (model|on this endpoint)|use /\S*chat/completions`)
	keyWords         = regexp.MustCompile(`(?i)invalid_api_key|invalid api key|incorrect api key|api key is invalid|authentication_error`)
)

// classifyRefusal says why a member refused, or "" when the answer says
// nothing about what the account may ask for: a bad request, a rate limit or
// a server fault is not a capability.
func classifyRefusal(status int, body []byte) string {
	switch status {
	case http.StatusUnauthorized:
		return RefusalKey
	case http.StatusForbidden:
		return RefusalPermission
	case http.StatusNotFound:
		return RefusalMissing
	}
	switch {
	case unsupportedWords.Match(body):
		return RefusalProtocol
	case missingWords.Match(body):
		return RefusalMissing
	case keyWords.Match(body):
		return RefusalKey
	case permissionWords.Match(body):
		return RefusalPermission
	}
	return ""
}
