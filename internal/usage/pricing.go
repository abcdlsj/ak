package usage

import (
	"sync"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/modelmeta"
)

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

// defaultPrices loads the table once, from the catalog ak keeps for both prices
// and model numbers: a fresh cache answers at once, a stale one is refetched,
// and a failed fetch leaves the old prices in place — an old price beats
// reporting every row as unpriced offline.
func defaultPrices() priceBook {
	priceOnce.Do(func() {
		_ = modelmeta.Refresh()
		book := priceBook{}
		for id, p := range modelmeta.Prices() {
			book[id] = modelPrice{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite}
		}
		prices = book
	})
	return prices
}

// lookup finds a model's price, trying the forms relays commonly use. The
// spellings are modelmeta's, so a model ak knows a window for is a model it can
// price, and the other way round.
func (b priceBook) lookup(model string) (modelPrice, bool) {
	for _, cand := range modelmeta.Candidates(model) {
		if p, ok := b[cand]; ok {
			return p, true
		}
	}
	return modelPrice{}, false
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
// discount on the catalog price, then the catalog price itself.
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
