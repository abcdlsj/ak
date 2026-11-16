// Package modelmeta knows a model's context window and reply limit.
//
// Every engine keeps those two numbers in a file or a variable of its own, and
// each guesses in its own way when it has neither: pi takes a model for a 128K
// one and caps every reply at 16384 tokens, Claude Code guesses a window and
// asks for 32000 at most, and codex takes 272K. The numbers come from
// models.dev, the table ak already reads for prices, and a provider may state
// its own.
package modelmeta

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

// url is models.dev's catalog. ak keeps its own slice of it, apart from the
// prices usage reads: a launch must answer from disk, and neither table should
// make the other wait for a fetch.
const url = "https://models.dev/api.json"

// ttl is how long the cached table is trusted before a refetch.
const ttl = 24 * time.Hour

// leastWindow is the smallest context window taken at its word. Less cannot
// hold a system prompt, let alone a turn, and an engine told it compacts at
// once or refuses to send: the smallest real chat models are about 4096.
const leastWindow = 2048

// Facts are a model's numbers, in tokens. 0 means not known.
type Facts struct {
	Context   int `json:"context,omitempty"`
	MaxOutput int `json:"max_output,omitempty"`
}

// For is what ak knows about one of p's models: the provider's own numbers win
// over the table's.
func For(p config.Provider, model string) Facts {
	f := Facts{Context: p.ContextWindow, MaxOutput: p.MaxTokens}
	known, ok := Lookup(model)
	if !ok {
		return f
	}
	if f.Context == 0 {
		f.Context = known.Context
	}
	if f.MaxOutput == 0 {
		f.MaxOutput = known.MaxOutput
	}
	return f
}

// Lookup returns what the cached table says about model, trying the spellings
// a relay commonly uses. It never reaches the network: a launch and a sync must
// not wait on one, so a cold cache answers nothing and the engine keeps its own
// default. Refresh fills the cache.
func Lookup(model string) (Facts, bool) {
	table := cached()
	for _, cand := range Candidates(model) {
		if f, ok := table[cand]; ok && f != (Facts{}) {
			return f, true
		}
	}
	return Facts{}, false
}

// Candidates lists the ids to try for a model, most specific first: as given,
// lowercased, without a vendor/ prefix, then without a relay suffix. It is the
// same list ak prices a row by, so a model one resolves is a model the other
// resolves too.
func Candidates(model string) []string {
	var out []string
	seen := map[string]bool{}
	push := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range []string{model, strings.ToLower(model)} {
		push(m)
		if i := strings.LastIndex(m, "/"); i >= 0 {
			push(m[i+1:])
		}
	}
	for _, m := range append([]string(nil), out...) {
		for _, suffix := range []string{"-latest", "-preview", "-thinking", "-code"} {
			push(strings.TrimSuffix(m, suffix))
		}
	}
	return out
}

// Refresh brings the table up to date, fetching it when the cache is older than
// a day. A failure is not fatal, and the caller says so: an old table answers
// better than none, and every engine still has its own default.
func Refresh() error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if fresh, ok := readFresh(path); ok {
		setTable(path, fresh)
		return nil
	}
	fetched, err := fetch()
	if err != nil {
		return err
	}
	if err := writeTable(path, fetched); err != nil {
		return err
	}
	setTable(path, fetched)
	return nil
}

// cachePath is where the table is kept, beside the config and the price cache.
func cachePath() (string, error) {
	dir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "model-meta.json"), nil
}

// readFresh reads the cached table when it is younger than the TTL.
func readFresh(path string) (map[string]Facts, bool) {
	fi, err := os.Stat(path)
	if err != nil || time.Since(fi.ModTime()) >= ttl {
		return nil, false
	}
	t := readTable(path)
	return t, len(t) > 0
}

// readTable reads whatever the cache holds, however old: an old window beats
// none, and Refresh replaces it as soon as models.dev answers.
func readTable(path string) map[string]Facts {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var t map[string]Facts
	if json.Unmarshal(data, &t) != nil {
		return nil
	}
	return t
}

func writeTable(path string, t map[string]Facts) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.AtomicWrite(path, data, 0o600)
}

// doc is the part of models.dev's api.json ak reads: every provider's models
// with their limits.
type doc map[string]struct {
	Models map[string]struct {
		Limit *limit `json:"limit"`
	} `json:"models"`
}

// limit is models.dev's limit block: the whole window, the most a prompt may
// hold when that is less, and the longest reply.
type limit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

func fetch() (map[string]Facts, error) {
	// Short on purpose: a sync must not hang on a slow network, and a missed
	// table only means the engines keep their own defaults until the next one.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var d doc
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return buildTable(d), nil
}

// buildTable flattens the document into one table. Providers are visited in
// name order and the first row for an id wins, so the result does not depend on
// map order: a model several hosts list takes the first host's numbers, which
// differ only where a relay states caps of its own.
func buildTable(d doc) map[string]Facts {
	names := make([]string, 0, len(d))
	for n := range d {
		names = append(names, n)
	}
	sort.Strings(names)

	out := map[string]Facts{}
	for _, n := range names {
		for id, m := range d[n].Models {
			if _, ok := out[id]; ok {
				continue
			}
			if f := factsOf(m.Limit); f != (Facts{}) {
				out[id] = f
			}
		}
	}
	return out
}

// factsOf reads one limit block, 0 where it says nothing.
func factsOf(l *limit) Facts {
	if l == nil {
		return Facts{}
	}
	return Facts{Context: window(*l), MaxOutput: clamp(l.Output)}
}

// window is the tokens a prompt may hold: the input limit where models.dev
// gives one (gpt-5's 272K of its 400K), else the whole context. Either limit
// under leastWindow says nothing, so the other answers, or none does.
func window(l limit) int {
	in, ctx := clamp(l.Input), clamp(l.Context)
	switch {
	case in > 0 && ctx > 0 && ctx < in:
		return ctx
	case in > 0:
		return in
	}
	return ctx
}

func clamp(n int) int {
	if n < leastWindow {
		return 0
	}
	return n
}

// memo is the table last read, kept for the process: a launch asks for a few
// models, and re-reading a table of a few thousand rows for each would cost
// more than the answer is worth. It is keyed by the file's path, size and
// mtime, so a cache Refresh wrote, or a test's own temporary cache, is read
// instead of an older answer.
var memo struct {
	mu    sync.Mutex
	path  string
	mod   time.Time
	size  int64
	table map[string]Facts
}

// cached is the table on disk, or nil when there is none. A table is never
// edited in place: a reader may keep the map it was handed.
func cached() map[string]Facts {
	path, err := cachePath()
	if err != nil {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if memo.table != nil && memo.path == path && memo.size == fi.Size() && memo.mod.Equal(fi.ModTime()) {
		return memo.table
	}
	t := readTable(path)
	memo.path, memo.mod, memo.size, memo.table = path, fi.ModTime(), fi.Size(), t
	return t
}

// setTable replaces the memo after a write, so this process reads what it just
// fetched without looking at the file again.
func setTable(path string, t map[string]Facts) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	memo.path, memo.mod, memo.size, memo.table = path, fi.ModTime(), fi.Size(), t
}
