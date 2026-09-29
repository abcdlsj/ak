package quota

import (
	"context"
	"fmt"
	"strings"
)

// builtins lists the vendors ak knows a balance API for. Auto-detection uses
// Match on the provider's endpoint host; a provider may also name one with its
// quota field.
var builtins = []Source{deepseek{}, openrouter{}, moonshot{}, siliconflow{}}

// deepseek: GET /user/balance ->
// {"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"…"}]}
type deepseek struct{}

func (deepseek) ID() string             { return "deepseek" }
func (deepseek) Match(host string) bool { return strings.Contains(host, "deepseek") }

func (deepseek) Fetch(ctx context.Context, base, key string) (Quota, error) {
	var out struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency     string    `json:"currency"`
			TotalBalance flexFloat `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := getJSON(ctx, apiRoot(base)+"/user/balance", key, &out); err != nil {
		return Quota{}, err
	}
	if len(out.BalanceInfos) == 0 {
		return Quota{}, fmt.Errorf("the response carried no balance")
	}
	bi := out.BalanceInfos[0]
	v := float64(bi.TotalBalance)
	return Quota{Kind: "balance", Currency: bi.Currency, Balance: &v}, nil
}

// openrouter: GET /api/v1/key -> {"data":{"limit":…,"usage":…,"limit_remaining":…}}
// A key with no limit is credit-based, so the balance comes from /api/v1/credits
// as total_credits - total_usage.
type openrouter struct{}

func (openrouter) ID() string             { return "openrouter" }
func (openrouter) Match(host string) bool { return strings.Contains(host, "openrouter") }

func (openrouter) Fetch(ctx context.Context, base, key string) (Quota, error) {
	root := apiRoot(base)
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

func (moonshot) Fetch(ctx context.Context, base, key string) (Quota, error) {
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

// siliconflow: GET /v1/user/info ->
// {"code":20000,"data":{"balance":"…","totalBalance":"…","chargeBalance":"…"}}
type siliconflow struct{}

func (siliconflow) ID() string             { return "siliconflow" }
func (siliconflow) Match(host string) bool { return strings.Contains(host, "siliconflow") }

func (siliconflow) Fetch(ctx context.Context, base, key string) (Quota, error) {
	var out struct {
		Code int `json:"code"`
		Data struct {
			Balance       flexFloat `json:"balance"`
			TotalBalance  flexFloat `json:"totalBalance"`
			ChargeBalance flexFloat `json:"chargeBalance"`
		} `json:"data"`
	}
	if err := getJSON(ctx, apiRoot(base)+"/v1/user/info", key, &out); err != nil {
		return Quota{}, err
	}
	v := float64(out.Data.TotalBalance)
	if v == 0 {
		v = float64(out.Data.Balance)
	}
	return Quota{Kind: "balance", Currency: "CNY", Balance: &v,
		Detail: fmt.Sprintf("charged %g", float64(out.Data.ChargeBalance))}, nil
}
