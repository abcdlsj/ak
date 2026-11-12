package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// usageDays is how long the service keeps its daily usage: a year and a day,
// so the UI can draw a whole year as a calendar.
const usageDays = 366

// Tokens are a reply's token counts. Input excludes what was read from or
// written to the cache, which are counted apart, whatever the protocol.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// UsageRow is one day's use of one route by one token, at one account.
type UsageRow struct {
	Day      string `json:"day"` // YYYY-MM-DD, the server's local date
	Token    string `json:"token"`
	Route    string `json:"route"`   // the model name asked for
	Account  string `json:"account"` // the account that answered; empty when none did
	Model    string `json:"model"`   // the model it was asked for
	Requests int64  `json:"requests"`
	Failed   int64  `json:"failed"`
	Tokens
}

func (r UsageRow) key() string {
	return strings.Join([]string{r.Day, r.Token, r.Route, r.Account, r.Model}, "\x00")
}

// meter counts the service's usage by day, token, route and account, and
// keeps it in a file between runs.
type meter struct {
	mu    sync.Mutex
	rows  map[string]*UsageRow
	path  string
	dirty bool
}

func newMeter(dir string) *meter {
	m := &meter{rows: map[string]*UsageRow{}}
	if dir != "" {
		m.path = filepath.Join(dir, "service-usage.json")
	}
	return m
}

// add counts one request.
func (m *meter) add(r UsageRow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := r.key()
	row := m.rows[k]
	if row == nil {
		row = &UsageRow{Day: r.Day, Token: r.Token, Route: r.Route, Account: r.Account, Model: r.Model}
		m.rows[k] = row
	}
	row.Requests += r.Requests
	row.Failed += r.Failed
	row.Input += r.Input
	row.Output += r.Output
	row.CacheRead += r.CacheRead
	row.CacheWrite += r.CacheWrite
	m.dirty = true
}

// Since is every row from the last days days (today among them), for one
// token or, with token empty, all, newest day first.
func (m *meter) since(days int, token string) []UsageRow {
	from := time.Now().AddDate(0, 0, 1-days).Format(time.DateOnly)
	m.mu.Lock()
	out := make([]UsageRow, 0, len(m.rows))
	for _, r := range m.rows {
		if r.Day >= from && (token == "" || r.Token == token) {
			out = append(out, *r)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day > out[j].Day
		}
		return out[i].key() < out[j].key()
	})
	return out
}

// load reads what an earlier run counted.
func (m *meter) load() {
	if m.path == "" {
		return
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var rows []UsageRow
	if json.Unmarshal(b, &rows) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		r := r
		m.rows[r.key()] = &r
	}
}

// save writes the rows of the kept days when they changed.
func (m *meter) save() {
	if m.path == "" {
		return
	}
	m.mu.Lock()
	if !m.dirty {
		m.mu.Unlock()
		return
	}
	from := time.Now().AddDate(0, 0, -usageDays).Format(time.DateOnly)
	rows := make([]UsageRow, 0, len(m.rows))
	for k, r := range m.rows {
		if r.Day < from {
			delete(m.rows, k)
			continue
		}
		rows = append(rows, *r)
	}
	m.dirty = false
	m.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].key() < rows[j].key() })
	data, err := json.Marshal(rows)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(m.path), 0o700); err == nil {
			err = config.AtomicWrite(m.path, data, 0o600)
		}
	}
	if err != nil {
		m.mu.Lock()
		m.dirty = true
		m.mu.Unlock()
	}
}

// tap passes a reply to the client unchanged, reading on the way the
// status and the usage the reply reports.
type tap struct {
	http.ResponseWriter
	status int
	stream bool
	head   []byte // a stream's incomplete event, or the start of a plain body
	tokens Tokens
}

// tapBody bounds how much of a plain reply is kept to read its usage.
const tapBody = 4 << 20

func newTap(w http.ResponseWriter) *tap { return &tap{ResponseWriter: w} }

func (t *tap) WriteHeader(code int) {
	if t.status == 0 {
		t.status = code
		t.stream = isEventStream(t.Header())
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *tap) Write(b []byte) (int, error) {
	if t.status == 0 {
		t.WriteHeader(http.StatusOK)
	}
	n, err := t.ResponseWriter.Write(b)
	if t.stream {
		t.head = append(t.head, b[:n]...)
		for {
			i, sep := eventBoundary(t.head)
			if i < 0 {
				break
			}
			t.read(eventData(t.head[:i]))
			t.head = t.head[i+sep:]
		}
	} else if len(t.head) < tapBody {
		t.head = append(t.head, b[:n]...)
	}
	return n, err
}

func (t *tap) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// done reads what is left and answers the usage seen.
func (t *tap) done() Tokens {
	t.read(eventData(t.head))
	if !t.stream {
		t.read(t.head)
	}
	t.head = nil
	return t.tokens
}

// reportedUsage is a usage object in any of the protocols: Anthropic's
// input/output with cache counts apart, or OpenAI's with the cached share
// inside the input.
type reportedUsage struct {
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	InputDetails  struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	PromptDetails    struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// tokens normalizes a usage object; ok is false when it says nothing.
func (u reportedUsage) tokens() (Tokens, bool) {
	switch {
	case u.PromptTokens > 0 || u.CompletionTokens > 0:
		return Tokens{Input: u.PromptTokens - u.PromptDetails.Cached, Output: u.CompletionTokens, CacheRead: u.PromptDetails.Cached}, true
	case u.InputDetails.Cached > 0:
		return Tokens{Input: u.InputTokens - u.InputDetails.Cached, Output: u.OutputTokens, CacheRead: u.InputDetails.Cached}, true
	case u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheRead > 0:
		return Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation}, true
	}
	return Tokens{}, false
}

// read folds the usage one event or body reports into what was seen. Each
// count is the largest reported: streams repeat or grow them.
func (t *tap) read(data []byte) {
	if len(data) == 0 || data[0] != '{' {
		return
	}
	var v struct {
		Usage   *reportedUsage `json:"usage"`
		Message *struct {
			Usage *reportedUsage `json:"usage"`
		} `json:"message"`
		Response *struct {
			Usage *reportedUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &v) != nil {
		return
	}
	for _, u := range []*reportedUsage{v.Usage, msgUsage(v.Message), msgUsage(v.Response)} {
		if u == nil {
			continue
		}
		if n, ok := u.tokens(); ok {
			t.tokens.Input = max(t.tokens.Input, n.Input)
			t.tokens.Output = max(t.tokens.Output, n.Output)
			t.tokens.CacheRead = max(t.tokens.CacheRead, n.CacheRead)
			t.tokens.CacheWrite = max(t.tokens.CacheWrite, n.CacheWrite)
		}
	}
}

func msgUsage(m *struct {
	Usage *reportedUsage `json:"usage"`
}) *reportedUsage {
	if m == nil {
		return nil
	}
	return m.Usage
}
