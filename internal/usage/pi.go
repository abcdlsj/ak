package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// piSource reads pi's session logs under <agent-dir>/sessions. The agent
// directory is PI_CODING_AGENT_DIR when set, else ~/.pi/agent; the session
// directory is PI_CODING_AGENT_SESSION_DIR when set.
type piSource struct{}

func (piSource) Engine() string { return "pi" }

func (piSource) Roots(cfg *config.Config, home string) []string {
	if d := os.Getenv(provider.EnvPiHome); d != "" {
		home = config.ExpandHome(d)
	} else {
		home = filepath.Join(home, ".pi", "agent")
	}
	if d := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
		return []string{config.ExpandHome(d)}
	}
	return []string{filepath.Join(home, "sessions")}
}

// Provider attributes a pi session: an ak-managed provider records its
// provider id (ak-<name>), so it maps back exactly; one pinned to an existing
// pi provider shares that provider's id with the agent's own use, so it maps
// only when a single ak provider names it.
func (piSource) Provider(b bucket, a *attribution) string {
	if n, ok := a.piIDs[b.RawProvider]; ok {
		return n
	}
	return b.RawProvider
}

func (piSource) NewParser(state []byte) LineParser {
	p := &piParser{}
	if len(state) > 0 {
		_ = json.Unmarshal(state, &p.st)
	}
	return p
}

// piParser carries the provider and model across lines: a model_change updates
// them, and an entry that omits them inherits the latest.
type piParser struct {
	st struct {
		RawProvider string `json:"provider,omitempty"`
		Model       string `json:"model,omitempty"`
	}
}

func (p *piParser) State() []byte {
	b, _ := json.Marshal(p.st)
	return b
}

// piUsage is pi's usage object; cost is ignored because ak prices from
// models.dev and the provider's own pricing overrides.
type piUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	Reasoning   int64 `json:"reasoning"`
	TotalTokens int64 `json:"totalTokens"`
}

func (p *piParser) Parse(line []byte) (Record, bool) {
	if !bytes.Contains(line, []byte(`"message"`)) &&
		!bytes.Contains(line, []byte(`"model_change"`)) &&
		!bytes.Contains(line, []byte(`"usage"`)) {
		return Record{}, false
	}
	var d struct {
		Type      string   `json:"type"`
		ID        string   `json:"id"`
		Timestamp string   `json:"timestamp"`
		Provider  string   `json:"provider"`
		ModelID   string   `json:"modelId"`
		Model     string   `json:"model"`
		Usage     *piUsage `json:"usage"`
		Message   *struct {
			Role      string   `json:"role"`
			Provider  string   `json:"provider"`
			Model     string   `json:"model"`
			Usage     *piUsage `json:"usage"`
			Timestamp int64    `json:"timestamp"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &d) != nil {
		return Record{}, false
	}

	switch d.Type {
	case "model_change":
		if d.Provider != "" {
			p.st.RawProvider = d.Provider
		}
		if d.ModelID != "" {
			p.st.Model = d.ModelID
		}
		return Record{}, false
	case "message":
		if d.Message == nil {
			return Record{}, false
		}
		if d.Message.Provider != "" {
			p.st.RawProvider = d.Message.Provider
		}
		if d.Message.Model != "" {
			p.st.Model = d.Message.Model
		}
		if d.Message.Role != "assistant" || d.Message.Usage == nil {
			return Record{}, false
		}
		return p.record(d.ID, d.Timestamp, d.Message.Timestamp, d.Message.Provider, d.Message.Model, d.Message.Usage)
	case "usage":
		// A usage entry records model-attributed usage that is not an
		// assistant message (e.g. cache warming); it carries its own ids.
		if d.Usage == nil {
			return Record{}, false
		}
		if d.Provider != "" {
			p.st.RawProvider = d.Provider
		}
		if d.Model != "" {
			p.st.Model = d.Model
		}
		return p.record(d.ID, d.Timestamp, 0, d.Provider, d.Model, d.Usage)
	}
	return Record{}, false
}

func (p *piParser) record(id, iso string, msgTS int64, rawProvider, model string, u *piUsage) (Record, bool) {
	if u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0 {
		return Record{}, false
	}
	t, ok := parseTime(iso)
	if !ok && msgTS > 0 {
		t, ok = time.UnixMilli(msgTS).Local(), true
	}
	if !ok {
		return Record{}, false
	}
	if rawProvider == "" {
		rawProvider = p.st.RawProvider
	}
	if model == "" {
		model = p.st.Model
	}
	if model == "" {
		model = unknownModel
	}
	return Record{
		// A fork clones entries verbatim, id and timestamp included; hashing
		// them keeps the copy from being counted twice.
		Key: hashKey("pi", id, iso, strconv.FormatInt(msgTS, 10),
			strconv.FormatInt(u.Input, 10), strconv.FormatInt(u.Output, 10),
			strconv.FormatInt(u.CacheRead, 10), strconv.FormatInt(u.CacheWrite, 10)),
		Time:        t,
		Model:       model,
		RawProvider: rawProvider,
		Tokens: Tokens{
			Input: u.Input, Output: u.Output,
			CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
			Thinking: u.Reasoning,
		},
	}, true
}

// piProviderNames maps a pi provider id to the ak provider using it. Unlike
// codex, an ak-managed provider's id is its own command's (ak-<name>), so it is
// never ambiguous; an id several ak providers name (a shared pi_provider) is
// dropped rather than credited to one.
func piProviderNames(cfg *config.Config) map[string]string {
	out := map[string]string{}
	ambiguous := map[string]bool{}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.Kind != config.KindPi {
			continue
		}
		id := provider.PiProviderID(name)
		if p.PiProvider != "" && !p.IsPool() {
			id = p.PiProvider
		}
		if _, dup := out[id]; dup {
			ambiguous[id] = true
		}
		out[id] = name
	}
	for id := range ambiguous {
		delete(out, id)
	}
	return out
}
