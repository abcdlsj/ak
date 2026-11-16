package usage

// A provider on a remote account asks an ak service for a model name the
// service chose, such as a route that falls back over several models. The
// engine's own log records that name, which is not a model: pricing it finds
// nothing, and the cost of everything that went through the service is lost.
// The service knows which model answered, so it is asked.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
)

// Served is what one model answered under one route on one day.
type Served struct {
	Model  string
	Tokens Tokens
}

// RemoteUsage is what the services behind remote accounts served, by day,
// local account and route: the route name an engine logged is the key.
type RemoteUsage map[RemoteKey][]Served

// RemoteKey names one route of one remote account on one day.
type RemoteKey struct {
	Day, Account, Route string
}

// remoteDays bounds how much history is asked for: the service keeps a year.
const remoteDays = 366

// remoteTimeout bounds one service's answer. A remote that does not answer
// leaves its rows as the logs have them rather than holding up the report.
const remoteTimeout = 5 * time.Second

// FetchRemoteUsage asks every remote account's service what it served in the
// last days days. An account that cannot be asked is reported and left out;
// its usage stays attributed to the name the engine logged.
func FetchRemoteUsage(ctx context.Context, cfg *config.Config, days int) (RemoteUsage, []error) {
	if days <= 0 || days > remoteDays {
		days = remoteDays
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  = RemoteUsage{}
		errs []error
	)
	for _, account := range cfg.AccountNames() {
		p, ok := cfg.AccountProvider(account)
		if !ok {
			continue
		}
		v, ok := cfg.Vendor(p)
		if !ok || v.Quota != quota.RemoteSource {
			continue
		}
		// A service that is down must not hold up the others: they are
		// asked at once, each bounded by its own timeout.
		wg.Add(1)
		go func() {
			defer wg.Done()
			served, err := askRemote(ctx, p, days)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", account, err))
			}
			for route, s := range served {
				k := RemoteKey{Account: account, Route: route.route, Day: route.day}
				out[k] = append(out[k], s...)
			}
		}()
	}
	wg.Wait()
	return out, errs
}

// routeDay is one route on one day, as a service names them.
type routeDay struct{ day, route string }

// askRemote reads one service's usage: rows of the shape /ak/v1/usage
// answers, folded into what each route served.
func askRemote(ctx context.Context, p config.Provider, days int) (map[routeDay][]Served, error) {
	key, err := secrets.Default().Resolve(p)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("no token")
	}
	base, err := remoteBase(p.BaseURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	u := fmt.Sprintf("%s/ak/v1/usage?days=%d", base, days)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	var body struct {
		Rows []struct {
			Day        string `json:"day"`
			Route      string `json:"route"`
			Model      string `json:"model"`
			Input      int64  `json:"input"`
			Output     int64  `json:"output"`
			CacheRead  int64  `json:"cache_read"`
			CacheWrite int64  `json:"cache_write"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s answered no usage: %w", u, err)
	}
	// One route may reach several models in a day, and one model several
	// times; the total of each is what decides how the day's tokens split.
	type acc struct {
		model  string
		tokens Tokens
	}
	totals := map[routeDay]map[string]*acc{}
	for _, r := range body.Rows {
		if r.Day == "" || r.Route == "" || r.Model == "" {
			continue
		}
		k := routeDay{r.Day, r.Route}
		if totals[k] == nil {
			totals[k] = map[string]*acc{}
		}
		a := totals[k][r.Model]
		if a == nil {
			a = &acc{model: r.Model}
			totals[k][r.Model] = a
		}
		a.tokens.add(Tokens{Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite})
	}
	out := map[routeDay][]Served{}
	for k, models := range totals {
		for _, a := range models {
			out[k] = append(out[k], Served{Model: a.model, Tokens: a.tokens})
		}
	}
	return out, nil
}

// remoteBase is where a remote's service answers: its endpoint, less the /v1
// the engines append.
func remoteBase(base string) (string, error) {
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	if u, err := url.Parse(base); err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is no service URL", base)
	}
	return base, nil
}

// Refine replaces the route name a row was recorded with by the models the
// service says answered under it. A route that served one model renames the
// row; one that served several splits the row's tokens across them in the
// proportions the service counted. The token total never changes, so the
// figure stays what it was — only its attribution gets finer.
//
// A row of an account the report does not cover is returned unchanged.
func Refine(rows []Row, ru RemoteUsage, cfg *config.Config) []Row {
	if len(ru) == 0 {
		return rows
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		account := ""
		if p, ok := cfg.Providers[r.Provider]; ok {
			account = p.Account
		}
		if account == "" {
			out = append(out, r)
			continue
		}
		served := ru[RemoteKey{Day: r.Date, Account: account, Route: r.Model}]
		if len(served) == 0 {
			out = append(out, r)
			continue
		}
		out = append(out, split(r, served)...)
	}
	return out
}

// split turns one row into the rows its route's share of each model implies.
func split(r Row, served []Served) []Row {
	if len(served) == 1 {
		r.Model = served[0].Model
		return []Row{r}
	}
	total := int64(0)
	for _, s := range served {
		total += s.Tokens.Total()
	}
	if total <= 0 {
		return []Row{r}
	}
	parts := make([]Row, 0, len(served))
	var given Tokens
	for i, s := range served {
		p := r
		p.Model = s.Model
		if i == len(served)-1 {
			// The last part takes what rounding left, so the split adds up.
			p.Tokens.sub(given)
		} else {
			p.Tokens = scaleTokens(r.Tokens, s.Tokens.Total(), total)
			given.add(p.Tokens)
		}
		p.Slots = nil
		parts = append(parts, p)
	}
	return parts
}

// scaleTokens is t scaled by part/whole, field by field so the parts of a
// field add up to it.
func scaleTokens(t Tokens, part, whole int64) Tokens {
	f := func(v int64) int64 { return v * part / whole }
	return Tokens{
		Input:      f(t.Input),
		Output:     f(t.Output),
		CacheRead:  f(t.CacheRead),
		CacheWrite: f(t.CacheWrite),
		Thinking:   f(t.Thinking),
	}
}
