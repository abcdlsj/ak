package quota

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// antigravity reads the plan agy itself reports.
//
// An antigravity provider has no endpoint and no key: the account is the
// provider's directory, agy signs in to it, and the allowance belongs to
// Google's plan rather than to a balance ak could ask a vendor for. agy answers
// it in print mode — `agy -p "/usage" --output-format json` — which is the same
// window a person sees in agy's own status line, reported by agy on its own
// credential. ak asks agy and nothing else: no second client, no keyring, no
// endpoint of its own.
//
// The mode matters. An unknown slash command is not an error to agy: it spends
// a model turn answering it, so a renamed `/usage` would quietly cost tokens
// and print a conversation. The reply is therefore read as agy's usage answer
// only when it says it is one.
type antigravity struct{}

func (antigravity) ID() string { return "antigravity" }

// Match never matches a host: an antigravity provider has no endpoint to detect
// a source from, and asking agy costs a few seconds of agy's own startup, so a
// provider names this one with quota = antigravity rather than getting it by
// default.
func (antigravity) Match(string) bool { return false }

// agyUsage is the part of agy's print-mode answer ak reads: the slash command
// it ran, and the allowance that command reported.
type agyUsage struct {
	Status  string `json:"status"`
	Error   string `json:"error"`
	Command struct {
		Name string `json:"name"`
		Data struct {
			Groups []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Buckets     []struct {
					Name              string   `json:"name"`
					Window            string   `json:"window"`
					RemainingFraction *float64 `json:"remaining_fraction"`
					ResetTime         string   `json:"reset_time"`
					Disabled          bool     `json:"disabled"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

func (antigravity) Fetch(ctx context.Context, p config.Provider, key string) (Quota, error) {
	if p.Kind != config.KindAntigravity {
		return Quota{}, fmt.Errorf("antigravity quota needs a provider of that kind")
	}
	if p.GeminiDir == "" {
		return Quota{}, fmt.Errorf("the provider has no gemini_dir, so no agy account to read")
	}
	// No credential means nothing to ask: agy would answer its sign-in screen.
	if auth := provider.ReadNativeAuth(p); !auth.SignedIn {
		return Quota{}, fmt.Errorf("agy is not signed in to %s; sign in with `ak login <provider>`",
			shortHome(p.GeminiDir))
	}
	eng := provider.EngineFor(config.KindAntigravity)
	if eng == nil {
		return Quota{}, fmt.Errorf("no antigravity engine in this build")
	}
	plan, err := eng.Launch("", p, provider.Literal(""), provider.Context{})
	if err != nil {
		return Quota{}, err
	}
	bin, err := engineBin(eng)
	if err != nil {
		return Quota{}, err
	}
	args := append(append([]string(nil), plan.Args...), "-p", "/usage", "--output-format", "json")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = engineEnv(plan.Env)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return Quota{}, errors.New(msg)
		}
		return Quota{}, err
	}
	return parseAgyUsage(out)
}

// parseAgyUsage turns agy's answer into a plan: one window per group and
// allowance, with the share already used and when it resets.
func parseAgyUsage(raw []byte) (Quota, error) {
	var doc agyUsage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		return Quota{}, fmt.Errorf("agy answered something other than its usage: %w", err)
	}
	if doc.Status != "" && doc.Status != "SUCCESS" {
		return Quota{}, fmt.Errorf("agy could not read the plan: %s", cmp.Or(doc.Error, doc.Status))
	}
	if doc.Command.Name != "usage" {
		// An unknown command is a model turn, not an error: say so rather than
		// report the turn's answer as a plan.
		return Quota{}, fmt.Errorf("agy ran %q instead of /usage, which spends a turn; ak does not read that as a plan",
			doc.Command.Name)
	}
	q := Quota{Kind: "plan"}
	for _, g := range doc.Command.Data.Groups {
		for _, b := range g.Buckets {
			if b.Disabled {
				continue
			}
			w := Window{Name: windowName(g.Name, b.Name, b.Window)}
			if b.RemainingFraction != nil {
				w.Used = usedShare(*b.RemainingFraction)
			}
			if t := parseAgyTime(b.ResetTime); !t.IsZero() {
				w.Resets = &t
			}
			q.Windows = append(q.Windows, w)
		}
	}
	if len(q.Windows) == 0 {
		return Quota{}, fmt.Errorf("agy listed no allowance; the account may have none")
	}
	return q, nil
}

// windowName names one allowance the way a person reads it: the group of models
// and the window it draws on, "Gemini Models 5h" shortened to "Gemini 5h".
func windowName(group, bucket, window string) string {
	g := strings.TrimSuffix(strings.TrimSuffix(group, " Models"), " models")
	w := window
	if w == "" {
		w = strings.TrimSuffix(bucket, " Limit Remaining")
	}
	return strings.TrimSpace(g + " " + w)
}

// usedShare turns the fraction left into the share used, as a percentage to one
// decimal.
func usedShare(remaining float64) float64 {
	used := 100 * (1 - remaining)
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return math.Round(used*10) / 10
}

func parseAgyTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// engineBin is the binary a launch would run: the one pinned in the
// environment, the one in settings, or the engine's name on PATH.
func engineBin(eng provider.Engine) (string, error) {
	if b := strings.TrimSpace(os.Getenv(eng.BinEnvVar())); b != "" {
		return b, nil
	}
	if b := strings.TrimSpace(eng.ConfiguredBin(akSettings())); b != "" {
		return config.ExpandHome(b), nil
	}
	if b, err := exec.LookPath(eng.BinName()); err == nil {
		return b, nil
	}
	return "", fmt.Errorf("%s not found; set %s or the engine's bin in ak settings", eng.BinName(), eng.BinEnvVar())
}

// akSettings is the settings read for the binary lookup, once: a quota run asks
// about a provider at a time, and the file does not change under it.
var akSettings = func() func() config.Settings {
	var (
		once sync.Once
		s    config.Settings
	)
	return func() config.Settings {
		once.Do(func() {
			if cfg, err := config.Load(); err == nil {
				s = cfg.Settings
			}
		})
		return s
	}
}()

// engineEnv is the environment a launch of the engine would have: this
// process's, with what the plan unsets removed and what it sets added.
func engineEnv(plan provider.EnvPlan) []string {
	drop := make(map[string]bool, len(plan.Unset))
	for _, k := range plan.Unset {
		drop[k] = true
	}
	out := make([]string, 0, len(os.Environ())+len(plan.Set))
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); drop[k] {
			continue
		}
		out = append(out, kv)
	}
	for _, kv := range plan.Set {
		if kv.Secret && kv.Value == "" {
			continue
		}
		out = append(out, kv.Key+"="+kv.Value)
	}
	return out
}

// shortHome is a directory as a person types it: ~ for the home directory.
func shortHome(dir string) string {
	dir = config.ExpandHome(dir)
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(dir, home) {
		return dir
	}
	return "~" + strings.TrimPrefix(dir, home)
}

// lastLine is a command's error text without agy's own log lines above it.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	return s
}
