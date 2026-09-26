package usage

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

// modelsDevURL is the official model pricing source, also used by cc-switch.
const modelsDevURL = "https://models.dev/api.json"

// priceTTL is how long the cached table is trusted before a refetch.
const priceTTL = 24 * time.Hour

// modelPrice is the price per million tokens, in USD.
type modelPrice struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

func (p modelPrice) scale(f float64) modelPrice {
	return modelPrice{p.Input * f, p.Output * f, p.CacheRead * f, p.CacheWrite * f}
}

func (p modelPrice) cost(t Tokens) float64 {
	return float64(t.Input)/1e6*p.Input +
		float64(t.Output)/1e6*p.Output +
		float64(t.CacheRead)/1e6*p.CacheRead +
		float64(t.CacheWrite)/1e6*p.CacheWrite
}

// priceBook maps a model id to its price.
type priceBook map[string]modelPrice

var (
	priceOnce sync.Once
	prices    priceBook
)

// defaultPrices loads the table once: fresh cache, else models.dev, else a
// stale cache — an old price beats reporting everything as unpriced offline.
func defaultPrices() priceBook {
	priceOnce.Do(func() {
		cached, age, ok := readPriceCache()
		if ok && age < priceTTL {
			prices = cached
			return
		}
		if fresh, err := fetchPrices(); err == nil {
			prices = fresh
			writePriceCache(fresh)
			return
		}
		prices = cached
	})
	return prices
}

// lookup finds a model's price, trying the forms relays commonly use.
func (b priceBook) lookup(model string) (modelPrice, bool) {
	for _, cand := range priceCandidates(model) {
		if p, ok := b[cand]; ok {
			return p, true
		}
	}
	return modelPrice{}, false
}

// priceCandidates lists the ids to try, most specific first: as given,
// lowercased, without a vendor/ prefix, then without a relay suffix.
func priceCandidates(model string) []string {
	var out []string
	seen := map[string]bool{}
	push := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	base := []string{model, strings.ToLower(model)}
	for _, m := range base {
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

// costOf computes the cost of one row.
// priced=false means no pricing data was found. The caller must count it into
// unpricedTokens and must never treat it as zero, otherwise the total cost is
// silently understated.
func costOf(r Row, cfg *config.Config) (float64, bool) {
	price, ok := priceFor(r, cfg, defaultPrices())
	if !ok {
		return 0, false
	}
	return price.cost(r.Tokens), true
}

// priceFor resolves a row's price: an explicit per-provider price wins, then a
// discount on the models.dev price, then the models.dev price itself.
func priceFor(r Row, cfg *config.Config, book priceBook) (modelPrice, bool) {
	if p, ok := cfg.Providers[r.Provider]; ok && p.Pricing != nil {
		pr := *p.Pricing
		if pr.Input > 0 || pr.Output > 0 || pr.CacheRead > 0 || pr.CacheWrite > 0 {
			return modelPrice{pr.Input, pr.Output, pr.CacheRead, pr.CacheWrite}, true
		}
		if pr.Discount > 0 {
			base, ok := book.lookup(r.Model)
			return base.scale(pr.Discount), ok
		}
	}
	return book.lookup(r.Model)
}

func priceCachePath() string {
	dir, err := config.DataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "model-prices.json")
}

func readPriceCache() (priceBook, time.Duration, bool) {
	p := priceCachePath()
	fi, err := os.Stat(p)
	if err != nil {
		return nil, 0, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, 0, false
	}
	var m priceBook
	if json.Unmarshal(data, &m) != nil {
		return nil, 0, false
	}
	return m, time.Since(fi.ModTime()), true
}

func writePriceCache(m priceBook) {
	p := priceCachePath()
	if p == "" {
		return
	}
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = config.AtomicWrite(p, data, 0o600)
	// The earlier table resolved duplicate ids in random map order.
	_ = os.Remove(filepath.Join(filepath.Dir(p), "model-pricing.json"))
}

// modelsDevCost mirrors models.dev's cost structure, in USD per million tokens.
type modelsDevCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type modelsDevProvider struct {
	Models map[string]struct {
		Cost *modelsDevCost `json:"cost"`
	} `json:"models"`
}

// firstParty are the vendors whose own listing wins when several providers on
// models.dev list the same model id at different prices.
var firstParty = []string{"anthropic", "openai", "google", "deepseek", "moonshotai", "zhipuai", "xai", "mistral"}

func fetchPrices() (priceBook, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(modelsDevURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc map[string]modelsDevProvider
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	return buildPriceBook(doc), nil
}

// buildPriceBook flattens models.dev into one table. Providers are visited in
// a fixed order — first-party vendors, then the rest alphabetically — and the
// first price for an id wins, so the result does not depend on map order.
func buildPriceBook(doc map[string]modelsDevProvider) priceBook {
	rank := map[string]int{}
	for i, v := range firstParty {
		rank[v] = i + 1
	}
	names := make([]string, 0, len(doc))
	for n := range doc {
		names = append(names, n)
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

	out := priceBook{}
	for _, n := range names {
		for id, m := range doc[n].Models {
			if m.Cost == nil {
				continue
			}
			if _, ok := out[id]; ok {
				continue
			}
			out[id] = modelPrice{deref(m.Cost.Input), deref(m.Cost.Output),
				deref(m.Cost.CacheRead), deref(m.Cost.CacheWrite)}
		}
	}
	return out
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}
