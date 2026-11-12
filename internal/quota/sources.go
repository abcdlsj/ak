package quota

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// These sources stay in Go: openrouter may need a second request,
// moonshot's currency follows the endpoint host, and opendesign takes two
// requests with a key of the account's own. The rest are TOML plugins
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

// opendesign: the Design Plan's windows are read on the control plane with
// the account's control key (the sign-in's "control" secret), not the model
// key, and take two requests: the active workspace, then its billing
// preflight ->
// {"balanceUsd":…,"codingPlan":{"windows":[
//
//	{"durationSeconds":18000,"usedCredits":…,"limitCredits":…,"resetsAt":"…"},…]}}
type opendesign struct{}

const opendesignControl = "https://amr-api.open-design.ai"

func (opendesign) ID() string             { return "opendesign" }
func (opendesign) Match(host string) bool { return strings.HasSuffix(host, "open-design.ai") }

func (opendesign) Fetch(ctx context.Context, p config.Provider, _ string) (Quota, error) {
	control := p.Secrets["control"]
	if control == "" {
		return Quota{}, fmt.Errorf("the Design Plan is read with the account's sign-in; run `ak login opendesign` rather than pasting a key")
	}
	var ws struct {
		Active string `json:"activeWorkspaceId"`
		ID     string `json:"workspaceId"`
	}
	if err := getJSON(ctx, opendesignControl+"/api/v1/workspaces/current", control, &ws); err != nil {
		return Quota{}, err
	}
	id := cmp.Or(ws.Active, ws.ID)
	if id == "" {
		return Quota{}, fmt.Errorf("the account has no active workspace")
	}
	var pre struct {
		Balance    *flexFloat `json:"balanceUsd"`
		CodingPlan struct {
			Windows []struct {
				Duration flexFloat `json:"durationSeconds"`
				Used     flexFloat `json:"usedCredits"`
				Limit    flexFloat `json:"limitCredits"`
				Resets   string    `json:"resetsAt"`
			} `json:"windows"`
		} `json:"codingPlan"`
	}
	if err := getJSON(ctx, opendesignControl+"/api/v1/workspaces/"+url.PathEscape(id)+"/billing/preflight", control, &pre); err != nil {
		return Quota{}, err
	}
	q := Quota{Kind: "plan", Currency: "USD"}
	if pre.Balance != nil {
		v := float64(*pre.Balance)
		q.Balance = &v
	}
	for _, w := range pre.CodingPlan.Windows {
		if w.Limit <= 0 {
			continue
		}
		win := Window{Name: spanName(time.Duration(w.Duration) * time.Second), Used: min(100, 100*float64(w.Used)/float64(w.Limit))}
		win.Resets = parseTime(w.Resets)
		q.Windows = append(q.Windows, win)
	}
	return q, nil
}

// spanName names a window by its length: 5h, 7d.
func spanName(d time.Duration) string {
	switch {
	case d <= 0:
		return "window"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

// remote: an ak service, GET <root>/ak/v1/quota with the account's token ->
// {"accounts": [Quota…]}, the readings of the accounts the token reaches.
// It is named only: an ak service has no host of its own to detect. The
// service may sit under a path, so the base URL is kept, less a /v1.
type remote struct{}

// RemoteSource is the id a remote's vendor names as its quota source.
const RemoteSource = "ak"

func (remote) ID() string        { return RemoteSource }
func (remote) Match(string) bool { return false }

func (remote) Fetch(ctx context.Context, p config.Provider, key string) (Quota, error) {
	var r struct {
		Accounts []Quota `json:"accounts"`
	}
	base := strings.TrimSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1")
	if err := getJSON(ctx, base+"/ak/v1/quota", key, &r); err != nil {
		return Quota{}, err
	}
	q := Quota{Kind: "remote", Detail: fmt.Sprintf("%d accounts", len(r.Accounts))}
	for _, a := range r.Accounts {
		a.Accounts = nil
		q.Accounts = append(q.Accounts, a)
	}
	return q, nil
}
