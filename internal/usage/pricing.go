package usage

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// modelsDevURL is the official model pricing source, also used by cc-switch.
const modelsDevURL = "https://models.dev/api.json"

// modelPrice is the price per million tokens, in USD.
type modelPrice struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

var (
	priceOnce  sync.Once
	priceTable map[string]modelPrice
	priceMu    sync.RWMutex
)

// modelsDevCost mirrors models.dev's cost structure.
type modelsDevCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type modelsDevModel struct {
	Cost *modelsDevCost `json:"cost"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

// loadPrices loads the pricing table. It prefers the local cache (within 24h)
// and otherwise fetches from models.dev.
func loadPrices() {
	priceOnce.Do(func() {
		priceTable = map[string]modelPrice{}
		if cached, ok := readPriceCache(); ok {
			priceTable = cached
			return
		}
		if fresh, err := fetchPrices(); err == nil {
			priceTable = fresh
			writePriceCache(fresh)
		}
	})
}

func priceCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "ak", "model-pricing.json")
}

func readPriceCache() (map[string]modelPrice, bool) {
	p := priceCachePath()
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > 24*time.Hour {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var m map[string]modelPrice
	if json.Unmarshal(data, &m) != nil {
		return nil, false
	}
	return m, true
}

func writePriceCache(m map[string]modelPrice) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "ak")
	_ = os.MkdirAll(dir, 0o700)
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.WriteFile(priceCachePath(), data, 0o600)
}

func fetchPrices() (map[string]modelPrice, error) {
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

	// models.dev's cost unit is already USD per million tokens, so store it as-is.
	out := map[string]modelPrice{}
	for _, prov := range doc {
		for id, m := range prov.Models {
			if m.Cost == nil {
				continue
			}
			out[id] = modelPrice{
				Input:      deref(m.Cost.Input),
				Output:     deref(m.Cost.Output),
				CacheRead:  deref(m.Cost.CacheRead),
				CacheWrite: deref(m.Cost.CacheWrite),
			}
		}
	}
	return out, nil
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// costOf computes the cost of one row.
// priced=false means no pricing data was found. The caller must count it into
// unpricedTokens and must never treat it as zero, otherwise the total cost is
// silently understated.
func costOf(r Row, cfg *config.Config) (float64, bool) {
	t := r.Tokens

	// 1. An explicit provider override takes precedence.
	if p, ok := cfg.Providers[r.Provider]; ok && p.Pricing != nil {
		pr := *p.Pricing
		if pr.Input > 0 || pr.Output > 0 || pr.CacheRead > 0 || pr.CacheWrite > 0 {
			return calcCost(t, pr.Input, pr.Output, pr.CacheRead, pr.CacheWrite), true
		}
		// Discount mode: apply the factor on top of the models.dev price.
		if pr.Discount > 0 {
			base, ok := lookupPrice(r.Model)
			if ok {
				return calcCost(t, base.Input*pr.Discount, base.Output*pr.Discount,
					base.CacheRead*pr.Discount, base.CacheWrite*pr.Discount), true
			}
			return 0, false
		}
	}

	// 2. models.dev。
	base, ok := lookupPrice(r.Model)
	if !ok {
		return 0, false
	}
	return calcCost(t, base.Input, base.Output, base.CacheRead, base.CacheWrite), true
}

func calcCost(t Tokens, in, out, cacheRead, cacheWrite float64) float64 {
	return float64(t.Input)/1e6*in +
		float64(t.Output)/1e6*out +
		float64(t.CacheRead)/1e6*cacheRead +
		float64(t.CacheWrite)/1e6*cacheWrite
}

// lookupPrice looks up a model's pricing. When not found it tries a few common
// naming variants.
func lookupPrice(model string) (modelPrice, bool) {
	loadPrices()
	priceMu.RLock()
	defer priceMu.RUnlock()

	if p, ok := priceTable[model]; ok {
		return p, true
	}
	// Relay services often append a suffix to model names; strip it and retry.
	for _, suffix := range []string{"-latest", "-preview", "-thinking", "-code"} {
		if strings.HasSuffix(model, suffix) {
			trimmed := strings.TrimSuffix(model, suffix)
			if p, ok := priceTable[trimmed]; ok {
				return p, true
			}
		}
	}
	return modelPrice{}, false
}
