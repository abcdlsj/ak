// Package usage counts token usage for claude and codex.
//
// The data source is local session logs, not proxy forwarding: cc-switch
// accounts for usage through a self-hosted proxy. ak runs no proxy, so it
// parses the jsonl files under ~/.claude/projects and ~/.codex/sessions.
//
// Scanning produces cached buckets keyed by session; attribution to an ak
// provider and pricing happen afterwards, on every load, so a hook installed
// later or a pricing change applies to history without rebuilding the cache.
package usage

import (
	"os"
	"sort"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// Tokens is a token breakdown. Input excludes cache reads and writes, so the
// four billable fields never overlap.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	// Thinking is informational: it is already part of Output.
	Thinking int64 `json:"thinking"`
}

// Total is the billable amount: cache reads and writes are counted separately,
// matching models.dev's billing basis.
func (t Tokens) Total() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Thinking += o.Thinking
}

func (t *Tokens) sub(o Tokens) {
	t.Input -= o.Input
	t.Output -= o.Output
	t.CacheRead -= o.CacheRead
	t.CacheWrite -= o.CacheWrite
	t.Thinking -= o.Thinking
}

// Row is usage for one (date, provider, model, engine), after attribution.
type Row struct {
	Date     string `json:"date"`     // YYYY-MM-DD in local time
	Provider string `json:"provider"` // ak provider name; unknown means unattributable
	Model    string `json:"model"`
	Engine   string `json:"engine"` // claude | codex
	Tokens   Tokens `json:"tokens"`
	Requests int    `json:"requests"`
}

// Summary is the aggregated view.
type Summary struct {
	TotalTokens int64         `json:"total_tokens"`
	TotalCost   float64       `json:"total_cost"`
	Requests    int           `json:"requests"`
	ByProvider  []ProviderRow `json:"by_provider"`
	ByModel     []ModelRow    `json:"by_model"`
	// ByDate is the heatmap data source, ascending by date.
	ByDate []DateRow `json:"by_date"`
	// UnpricedTokens is the token count with no pricing data, so the cost figure is an underestimate.
	UnpricedTokens int64 `json:"unpriced_tokens"`
}

// ProviderRow aggregates by provider.
type ProviderRow struct {
	Name   string  `json:"name"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// ModelRow aggregates by model.
type ModelRow struct {
	Model  string  `json:"model"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// DateRow aggregates by date, used by the heatmap.
type DateRow struct {
	Date   string  `json:"date"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// Filter narrows a summary. Zero values mean no restriction.
type Filter struct {
	Since    string // inclusive YYYY-MM-DD
	Provider string
}

// SinceDays returns the local date n-1 days before today, so n=7 covers the
// last seven calendar days including today. n<=0 means no limit.
func SinceDays(n int) string {
	if n <= 0 {
		return ""
	}
	return time.Now().AddDate(0, 0, -(n - 1)).Format(dateLayout)
}

// Load scans the logs (incrementally, via the cache) and returns attributed rows.
// The first scan parses the whole history; later ones only read new bytes.
func Load(cfg *config.Config) ([]Row, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	roots := map[Source][]string{}
	for _, src := range sources {
		roots[src] = src.Roots(cfg, home)
	}
	cache := loadCache()
	buckets := scan(listFiles(roots), cache)
	// A cache write failure does not affect the result.
	_ = saveCache(cache)
	return attribute(buckets, newAttribution(cfg)), nil
}

// Aggregate loads and summarizes everything.
func Aggregate(cfg *config.Config) (Summary, error) {
	rows, err := Load(cfg)
	if err != nil {
		return Summary{}, err
	}
	return Summarize(rows, cfg, Filter{}), nil
}

// attribution is what sources consult to name a bucket's ak provider.
type attribution struct {
	sessions sessionIndex
	// codexIDs maps a codex provider_id to the ak provider using it.
	codexIDs map[string]string
}

func newAttribution(cfg *config.Config) *attribution {
	return &attribution{sessions: loadSessionIndex(), codexIDs: codexProviderNames(cfg)}
}

// attribute resolves each bucket's ak provider and folds away session detail.
func attribute(buckets []bucket, a *attribution) []Row {
	type key struct{ date, provider, model, engine string }
	acc := map[key]*Row{}
	for _, b := range buckets {
		prov := ""
		if src := sourceFor(b.Engine); src != nil {
			prov = src.Provider(b, a)
		}
		// Unattributed usage ran on the engine's own default login.
		if prov == "" {
			prov = b.Engine
		}
		if prov == "" {
			prov = unknownProvider
		}
		k := key{b.Date, prov, b.Model, b.Engine}
		r := acc[k]
		if r == nil {
			r = &Row{Date: b.Date, Provider: prov, Model: b.Model, Engine: b.Engine}
			acc[k] = r
		}
		r.Tokens.add(b.Tokens)
		r.Requests += b.Requests
	}

	out := make([]Row, 0, len(acc))
	for _, r := range acc {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Engine < b.Engine
	})
	return out
}

// codexProviderNames maps a codex provider_id to the ak provider using it.
// An id shared by several ak providers is ambiguous and left unmapped.
func codexProviderNames(cfg *config.Config) map[string]string {
	out := map[string]string{}
	ambiguous := map[string]bool{}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.Kind != config.KindCodex {
			continue
		}
		id := provider.CodexProviderID(name, p)
		if _, dup := out[id]; dup {
			ambiguous[id] = true
		}
		out[id] = name
	}
	for id := range ambiguous {
		delete(out, id)
	}
	return out
}

// Summarize aggregates rows into the various views.
func Summarize(rows []Row, cfg *config.Config, f Filter) Summary {
	var s Summary
	byProvider := map[string]*ProviderRow{}
	byModel := map[string]*ModelRow{}
	byDate := map[string]*DateRow{}

	for _, r := range rows {
		if f.Since != "" && r.Date < f.Since {
			continue
		}
		if f.Provider != "" && r.Provider != f.Provider {
			continue
		}
		t := r.Tokens.Total()
		s.TotalTokens += t
		s.Requests += r.Requests
		cost, priced := costOf(r, cfg)
		if priced {
			s.TotalCost += cost
		} else {
			s.UnpricedTokens += t
		}

		pr := byProvider[r.Provider]
		if pr == nil {
			pr = &ProviderRow{Name: r.Provider}
			byProvider[r.Provider] = pr
		}
		pr.Tokens += t
		pr.Cost += cost

		mr := byModel[r.Model]
		if mr == nil {
			mr = &ModelRow{Model: r.Model}
			byModel[r.Model] = mr
		}
		mr.Tokens += t
		mr.Cost += cost

		dr := byDate[r.Date]
		if dr == nil {
			dr = &DateRow{Date: r.Date}
			byDate[r.Date] = dr
		}
		dr.Tokens += t
		dr.Cost += cost
	}

	s.ByProvider = sortedProviders(byProvider)
	s.ByModel = sortedModels(byModel)
	s.ByDate = sortedDates(byDate)
	return s
}

func sortedProviders(m map[string]*ProviderRow) []ProviderRow {
	out := make([]ProviderRow, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func sortedModels(m map[string]*ModelRow) []ModelRow {
	out := make([]ModelRow, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func sortedDates(m map[string]*DateRow) []DateRow {
	out := make([]DateRow, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}
