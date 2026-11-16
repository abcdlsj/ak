// Package modelmeta knows a model: its context window, the longest reply it may
// write, and what a million of its tokens cost.
//
// Every engine keeps the two numbers in a file or a variable of its own, and
// each guesses in its own way when it has neither: pi takes a model for a 128K
// one and caps every reply at 16384 tokens, Claude Code guesses a window and
// asks for 32000 at most, and codex takes 272K. Both sets of numbers come from
// models.dev in one document and live in one cache, so one download answers a
// sync and a usage report, and a model ak knows a window for is a model it can
// price.
package modelmeta

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// url is models.dev's catalog: every provider's models with their limits and
// their prices.
const url = "https://models.dev/api.json"

// ttl is how long the cached catalog is trusted before a refetch.
const ttl = 24 * time.Hour

// fetchTimeout is long enough for the catalog over a slow link, short enough
// that a sync does not hang on a network that is not there.
const fetchTimeout = 10 * time.Second

// cacheName is the catalog beside the config. It holds both tables, so a model
// priced by one is a model with numbers in the other.
const cacheName = "model-meta.json"

// legacyPriceCache is the price table ak kept in a file of its own, with its own
// fetch, before the two shared one document. It is reclaimed when a write
// happens, so nothing reads a table the catalog has replaced.
const legacyPriceCache = "model-prices.json"

// leastWindow is the smallest context window taken at its word. Less cannot
// hold a system prompt, let alone a turn, and an engine told it compacts at
// once or refuses to send: the smallest real chat models are about 4096.
const leastWindow = 2048

// Facts are a model's numbers, in tokens. 0 means not known.
type Facts struct {
	Context   int `json:"context,omitempty"`
	MaxOutput int `json:"max_output,omitempty"`
}

// Price is what a million tokens cost, in USD.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// catalog is what the cache holds: the two tables read from one document.
type catalog struct {
	Models map[string]Facts `json:"models"`
	Prices map[string]Price `json:"prices"`
}

// For is what ak knows about one of p's models: the provider's own numbers win
// over the catalog's.
func For(p config.Provider, model string) Facts {
	f := Facts{Context: p.ContextWindow, MaxOutput: p.MaxTokens}
	if known, ok := Lookup(model); ok {
		if f.Context == 0 {
			f.Context = known.Context
		}
		if f.MaxOutput == 0 {
			f.MaxOutput = known.MaxOutput
		}
	}
	return fit(f)
}

// fit drops a reply limit that cannot fit its window: a provider states the
// window while the catalog states the reply, and one of them would be wrong.
// Left out, the engine keeps its own.
func fit(f Facts) Facts {
	if f.Context > 0 && f.MaxOutput >= f.Context {
		f.MaxOutput = 0
	}
	return f
}

// Lookup returns what the cached catalog says about model, trying the spellings
// a relay commonly uses. It never reaches the network: a launch and a sync must
// not wait on one, so a cold cache answers nothing and the engine keeps its own
// default. Refresh fills the cache.
func Lookup(model string) (Facts, bool) {
	models := cached().Models
	for _, cand := range Candidates(model) {
		if f, ok := models[cand]; ok && f != (Facts{}) {
			return f, true
		}
	}
	return Facts{}, false
}

// Prices is what the cached catalog says a million tokens cost, keyed by model
// id. Like Lookup it never reaches the network, and an empty table means no
// price is known rather than a price of zero.
func Prices() map[string]Price {
	return cached().Prices
}

// Candidates lists the ids to try for a model, most specific first: as given,
// lowercased, without a vendor/ prefix, then without a relay suffix. One list
// serves both tables, so a model one resolves is a model the other resolves
// too.
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

// Refresh brings the catalog up to date, fetching it when the cache is older
// than a day. A failure is not fatal, and the caller says so: an old catalog
// answers better than none, and every engine still has its own default.
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

// cachePath is where the catalog is kept, beside the config.
func cachePath() (string, error) {
	dir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheName), nil
}

// readFresh reads the cached catalog when it is younger than the TTL.
func readFresh(path string) (catalog, bool) {
	fi, err := os.Stat(path)
	if err != nil || time.Since(fi.ModTime()) >= ttl {
		return catalog{}, false
	}
	c := readTable(path)
	return c, !c.empty()
}

// empty reports whether the catalog says nothing at all.
func (c catalog) empty() bool {
	return len(c.Models) == 0 && len(c.Prices) == 0
}

// readTable reads whatever the cache holds, however old: an old window beats
// none, and Refresh replaces it as soon as models.dev answers.
func readTable(path string) catalog {
	data, err := os.ReadFile(path)
	if err != nil {
		return catalog{}
	}
	var c catalog
	if json.Unmarshal(data, &c) != nil {
		return catalog{}
	}
	return c
}

func writeTable(path string, c catalog) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := config.AtomicWrite(path, data, 0o600); err != nil {
		return err
	}
	// The prices ak used to keep on their own are in this file now.
	_ = os.Remove(filepath.Join(filepath.Dir(path), legacyPriceCache))
	return nil
}

// doc is the part of models.dev's api.json ak reads: every provider's models
// with their limits and their prices.
type doc map[string]struct {
	Models map[string]row `json:"models"`
}

// row is one model as models.dev lists it, with whatever it says about its
// limits and its price.
type row struct {
	Limit *limit `json:"limit"`
	Cost  *cost  `json:"cost"`
}

// limit is models.dev's limit block: the whole window, the most a prompt may
// hold when that is less, and the longest reply.
type limit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

// cost is models.dev's price block, in USD per million tokens.
type cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

func fetch() (catalog, error) {
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return catalog{}, err
	}
	defer resp.Body.Close()
	return readDoc(resp)
}

// readDoc turns one answer into the tables, and refuses anything that is not a
// full catalog: a relay in front of models.dev answers an error as JSON of its
// own, and a table of no rows written over a good cache would leave every
// engine its own default until the next sync.
func readDoc(resp *http.Response) (catalog, error) {
	if resp.StatusCode != http.StatusOK {
		return catalog{}, fmt.Errorf("models.dev answered %s", resp.Status)
	}
	var d doc
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return catalog{}, err
	}
	c := buildTable(d)
	if c.empty() {
		return catalog{}, fmt.Errorf("models.dev listed no model")
	}
	return c, nil
}

// buildTable flattens the document into the two tables. Providers are visited
// in a fixed order — first-party vendors, then the rest by name — and the first
// row for an id wins, so the result does not depend on map order and a model
// takes its vendor's numbers rather than a relay's rounded ones.
//
// The two tables fall to the next host on their own: a host that lists a model
// but states nothing about its limits does not keep the row unpriced, and one
// that states no price does not keep the row without numbers.
func buildTable(d doc) catalog {
	names := make([]string, 0, len(d))
	for n := range d {
		names = append(names, n)
	}
	orderHosts(names)

	out := catalog{Models: map[string]Facts{}, Prices: map[string]Price{}}
	for _, n := range names {
		for id, m := range d[n].Models {
			if _, ok := out.Models[id]; !ok {
				if f := factsOf(m.Limit); f != (Facts{}) {
					out.Models[id] = f
				}
			}
			if _, ok := out.Prices[id]; !ok {
				if m.Cost != nil && *m.Cost != (cost{}) {
					out.Prices[id] = Price(*m.Cost)
				}
			}
		}
	}
	return out
}

// firstParty are the vendors whose own listing wins when several providers on
// models.dev list the same model id. Anything else may state a relay's
// numbers: 1M for a 131072 one, or a reply limit as long as the window.
var firstParty = []string{"anthropic", "openai", "google", "deepseek", "moonshotai", "zhipuai", "xai", "mistral"}

// orderHosts sorts provider names in the order rows are taken from them:
// first-party vendors first, then the rest alphabetically.
func orderHosts(names []string) {
	rank := make(map[string]int, len(firstParty))
	for i, v := range firstParty {
		rank[v] = i + 1
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := rank[names[i]], rank[names[j]]
		switch {
		case ri > 0 && rj > 0:
			return ri < rj
		case ri > 0 || rj > 0:
			return ri > 0
		}
		return names[i] < names[j]
	})
}

// factsOf reads one limit block, 0 where it says nothing.
func factsOf(l *limit) Facts {
	if l == nil {
		return Facts{}
	}
	return fit(Facts{Context: window(*l), MaxOutput: clamp(l.Output)})
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

// memo is the catalog last read, kept for the process: a launch asks for a few
// models, and re-reading a table of a few thousand rows for each would cost
// more than the answer is worth. It is keyed by the file's path, size and
// mtime, so a cache Refresh wrote, or a test's own temporary cache, is read
// instead of an older answer.
var memo struct {
	mu    sync.Mutex
	path  string
	mod   time.Time
	size  int64
	table catalog
}

// cached is the catalog on disk, or an empty one when there is none. A table is
// never edited in place: a reader may keep the maps it was handed.
func cached() catalog {
	path, err := cachePath()
	if err != nil {
		return catalog{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return catalog{}
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if memo.path == path && memo.size == fi.Size() && memo.mod.Equal(fi.ModTime()) {
		return memo.table
	}
	c := readTable(path)
	memo.path, memo.mod, memo.size, memo.table = path, fi.ModTime(), fi.Size(), c
	return c
}

// setTable replaces the memo after a write, so this process reads what it just
// fetched without looking at the file again.
func setTable(path string, c catalog) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	memo.path, memo.mod, memo.size, memo.table = path, fi.ModTime(), fi.Size(), c
}
