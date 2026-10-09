package quota

import (
	"context"
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// These sources stay in Go: openrouter may need a second request, and
// moonshot's currency follows the endpoint host. The rest are TOML plugins
// under plugins/.

// openrouter: GET /api/v1/key -> {"data":{"limit":…,"usage":…,"limit_remaining":…}}
// A key with no limit is credit-based, so the balance comes from /api/v1/credits
// as total_credits - total_usage.
type openrouter struct{}

func (openrouter) ID() string             { return "openrouter" }
func (openrouter) Match(host string) bool { return strings.Contains(host, "openrouter") }

func (openrouter) Fetch(ctx context.Context, p config.Provider, key string) (Quota, error) {
	root := apiRoot(p.BaseURL)
	if root == "" {
		root = "https://openrouter.ai"
	}
	var k struct {
		Data struct {
			Label          string     `json:"label"`
			Usage          flexFloat  `json:"usage"`
			Limit          *flexFloat `json:"limit"`
			LimitRemaining *flexFloat `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := getJSON(ctx, root+"/api/v1/key", key, &k); err != nil {
		return Quota{}, err
	}
	if k.Data.Limit != nil && k.Data.LimitRemaining != nil {
		bal, used, lim := float64(*k.Data.LimitRemaining), float64(k.Data.Usage), float64(*k.Data.Limit)
		return Quota{Kind: "balance", Currency: "USD", Balance: &bal, Used: &used, Limit: &lim, Detail: k.Data.Label}, nil
	}
	var c struct {
		Data struct {
			TotalCredits flexFloat `json:"total_credits"`
			TotalUsage   flexFloat `json:"total_usage"`
		} `json:"data"`
	}
	if err := getJSON(ctx, root+"/api/v1/credits", key, &c); err != nil {
		return Quota{}, err
	}
	credits, usage := float64(c.Data.TotalCredits), float64(c.Data.TotalUsage)
	bal := credits - usage
	return Quota{Kind: "balance", Currency: "USD", Balance: &bal, Used: &usage, Limit: &credits, Detail: k.Data.Label}, nil
}

// moonshot: GET /v1/users/me/balance ->
// {"code":0,"data":{"available_balance":"…","voucher_balance":"…","cash_balance":"…"}}
type moonshot struct{}

func (moonshot) ID() string             { return "moonshot" }
func (moonshot) Match(host string) bool { return strings.Contains(host, "moonshot") }

func (moonshot) Fetch(ctx context.Context, p config.Provider, key string) (Quota, error) {
	base := p.BaseURL
	var out struct {
		Code int `json:"code"`
		Data struct {
			AvailableBalance flexFloat `json:"available_balance"`
			VoucherBalance   flexFloat `json:"voucher_balance"`
			CashBalance      flexFloat `json:"cash_balance"`
		} `json:"data"`
	}
	if err := getJSON(ctx, apiRoot(base)+"/v1/users/me/balance", key, &out); err != nil {
		return Quota{}, err
	}
	currency := "USD"
	if strings.Contains(hostOf(base), ".cn") {
		currency = "CNY"
	}
	v := float64(out.Data.AvailableBalance)
	return Quota{
		Kind: "balance", Currency: currency, Balance: &v,
		Detail: fmt.Sprintf("cash %g · voucher %g", float64(out.Data.CashBalance), float64(out.Data.VoucherBalance)),
	}, nil
}
