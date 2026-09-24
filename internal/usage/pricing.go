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

// modelsDevURL 是官方模型定价源,cc-switch 也用这个。
const modelsDevURL = "https://models.dev/api.json"

// modelPrice 是每百万 token 的价格,单位美元。
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

// modelsDevCost 对应 models.dev 的 cost 结构。
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

// loadPrices 载入定价表。优先用本地缓存(24h 内),否则拉 models.dev。
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

	// models.dev 的 cost 单位已经是「美元每百万 token」,原样存即可。
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

// costOf 计算一行的成本。
// 返回 priced=false 表示查不到定价 —— 调用方必须把它计入 unpricedTokens,
// 绝不能当成 0 元,否则总成本会静默偏低。
func costOf(r Row, cfg *config.Config) (float64, bool) {
	t := r.Tokens

	// 1. 供应商显式覆盖优先。
	if p, ok := cfg.Providers[r.Provider]; ok && p.Pricing != nil {
		pr := *p.Pricing
		if pr.Input > 0 || pr.Output > 0 || pr.CacheRead > 0 || pr.CacheWrite > 0 {
			return calcCost(t, pr.Input, pr.Output, pr.CacheRead, pr.CacheWrite), true
		}
		// 折扣模式:在 models.dev 价格上打折。
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

// lookupPrice 查模型定价。找不到时尝试几种常见的命名变体。
func lookupPrice(model string) (modelPrice, bool) {
	loadPrices()
	priceMu.RLock()
	defer priceMu.RUnlock()

	if p, ok := priceTable[model]; ok {
		return p, true
	}
	// 中转站常给模型加后缀,剥掉再试一次。
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
