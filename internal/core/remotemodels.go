package core

// A provider on a remote account asks an ak service for a name the service
// chose. That name may be a route over several models rather than a model, so
// what the provider answers with is only known by asking the service. It is
// asked once and kept, so a list opens on what was last known and the ask
// happens behind it.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
)

// RemoteModels is what a remote service said each of its names answers with:
// the models behind a route, when it served more than one. A name serving one
// model is absent, since the name and the model are then the same answer.
type RemoteModels struct {
	// At is when the service last answered.
	At     time.Time           `json:"at"`
	Routes map[string][]string `json:"routes,omitempty"`
}

// remoteModelsTTL is how long what a service said is trusted before being
// asked again. A route's members change when its owner edits it, not by the
// minute, and the ask is only for display.
const remoteModelsTTL = 15 * time.Minute

// remoteModelsTimeout bounds one service's answer; a remote that does not
// answer leaves the last known answer in place and never holds up a screen.
const remoteModelsTimeout = 6 * time.Second

// Models returns the models a name answers with, or nil when the service did
// not say. It is the route resolver Serves takes.
func (r RemoteModels) Models(name string) []string { return r.Routes[name] }

// empty reports whether nothing is known.
func (r RemoteModels) empty() bool { return len(r.Routes) == 0 }

// RemoteRoutes is the last answer each remote account gave, loaded from disk
// so a list has something to show before any network call.
func RemoteRoutes(cfg *config.Config) RemoteModels {
	path, err := remoteModelsPath()
	if err != nil {
		return RemoteModels{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RemoteModels{}
	}
	var all map[string]RemoteModels
	if json.Unmarshal(data, &all) != nil {
		return RemoteModels{}
	}
	out := RemoteModels{Routes: map[string][]string{}}
	for _, account := range cfg.AccountNames() {
		if !isRemote(cfg, account) {
			continue
		}
		r := all[account]
		for name, models := range r.Routes {
			out.Routes[name] = models
		}
		out.At = r.At
	}
	return out
}

// FetchRemoteRoutes asks every remote account what its names answer with and
// keeps the answer. Accounts that cannot be asked keep what was known; the
// first error is returned for the caller to mention or ignore.
func FetchRemoteRoutes(ctx context.Context, cfg *config.Config) (RemoteModels, error) {
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		first error
		all   = map[string]RemoteModels{}
	)
	path, _ := remoteModelsPath()
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	for _, account := range cfg.AccountNames() {
		if !isRemote(cfg, account) {
			continue
		}
		p, ok := cfg.AccountProvider(account)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			routes, err := askRoutes(ctx, p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = fmt.Errorf("%s: %w", account, err)
				}
				return
			}
			all[account] = RemoteModels{At: time.Now(), Routes: routes}
		}()
	}
	wg.Wait()
	_ = writeRemoteModels(path, all)
	out := RemoteModels{Routes: map[string][]string{}}
	for _, account := range cfg.AccountNames() {
		for name, models := range all[account].Routes {
			out.Routes[name] = models
		}
		if at := all[account].At; at.After(out.At) {
			out.At = at
		}
	}
	return out, first
}

// Stale reports whether what is known is old enough to ask again.
func (r RemoteModels) Stale() bool { return time.Since(r.At) > remoteModelsTTL }

// isRemote reports whether an account is a signed-in ak service.
func isRemote(cfg *config.Config, account string) bool {
	p, ok := cfg.AccountProvider(account)
	if !ok {
		return false
	}
	v, ok := cfg.Vendor(p)
	return ok && v.Quota == quota.RemoteSource
}

// askRoutes reads one service's model list: each name, and the models a name
// that stands for several says it answers with.
func askRoutes(ctx context.Context, p config.Provider) (map[string][]string, error) {
	key, err := secrets.Default().Resolve(p)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("no token")
	}
	base := strings.TrimSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1")
	ctx, cancel := context.WithTimeout(ctx, remoteModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
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
		return nil, fmt.Errorf("/v1/models answered %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
			AK struct {
				Models []string `json:"models"`
			} `json:"ak"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("/v1/models answered no list: %w", err)
	}
	out := map[string][]string{}
	for _, m := range body.Data {
		if m.ID == "" || len(m.AK.Models) < 2 {
			continue
		}
		models := slices.Clone(m.AK.Models)
		slices.Sort(models)
		out[m.ID] = models
	}
	return out, nil
}

func remoteModelsPath() (string, error) {
	dir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remote-models.json"), nil
}

func writeRemoteModels(path string, all map[string]RemoteModels) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.AtomicWrite(path, data, 0o600)
}
