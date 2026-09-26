package usage

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"

	"github.com/abcdlsj/ak/internal/config"
)

// codexSource reads ~/.codex/{sessions,archived_sessions}/**/*.jsonl.
type codexSource struct{}

func (codexSource) Engine() string { return "codex" }

func (codexSource) Roots(cfg *config.Config, home string) []string {
	homes := []string{filepath.Join(home, ".codex")}
	for _, name := range cfg.Names() {
		if d := cfg.Providers[name].CodexHome; d != "" {
			homes = append(homes, config.ExpandHome(d))
		}
	}
	var roots []string
	for _, h := range homes {
		roots = append(roots, filepath.Join(h, "sessions"), filepath.Join(h, "archived_sessions"))
	}
	return roots
}

// Provider maps the provider_id recorded in session_meta to the ak provider;
// imported providers keep their original id, so the two can differ. An id
// with no ak provider is reported as-is.
func (codexSource) Provider(b bucket, a *attribution) string {
	if n, ok := a.codexIDs[b.RawProvider]; ok {
		return n
	}
	return b.RawProvider
}

func (codexSource) NewParser(state []byte) LineParser {
	p := &codexParser{}
	if len(state) > 0 {
		_ = json.Unmarshal(state, &p.st)
	}
	return p
}

// codexParser carries session context across lines: token_count events do not
// repeat the model or provider.
type codexParser struct {
	st struct {
		Model       string `json:"model,omitempty"`
		RawProvider string `json:"provider,omitempty"`
		// LastTotal is the cumulative total of the last token_count; codex
		// repeats the event with an unchanged total, which must not count twice.
		LastTotal int64 `json:"last_total,omitempty"`
	}
}

func (p *codexParser) State() []byte {
	b, _ := json.Marshal(p.st)
	return b
}

func (p *codexParser) Parse(line []byte) (Record, bool) {
	if !bytes.Contains(line, []byte(`"token_count"`)) &&
		!bytes.Contains(line, []byte(`"turn_context"`)) &&
		!bytes.Contains(line, []byte(`"session_meta"`)) {
		return Record{}, false
	}
	var d struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type          string `json:"type"`
			ModelProvider string `json:"model_provider"`
			Model         string `json:"model"`
			Info          *struct {
				Total struct {
					TotalTokens int64 `json:"total_tokens"`
				} `json:"total_token_usage"`
				// last_token_usage is this turn's increment; only it is summed.
				Last struct {
					Input     int64 `json:"input_tokens"`
					Cached    int64 `json:"cached_input_tokens"`
					Output    int64 `json:"output_tokens"`
					Reasoning int64 `json:"reasoning_output_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &d) != nil {
		return Record{}, false
	}

	switch {
	case d.Type == "session_meta":
		// A forked session repeats its parent's meta later on; the first one is ours.
		if p.st.RawProvider == "" {
			p.st.RawProvider = d.Payload.ModelProvider
		}
		return Record{}, false
	case d.Type == "turn_context":
		// The model can change mid-session, so track the latest.
		if d.Payload.Model != "" {
			p.st.Model = d.Payload.Model
		}
		return Record{}, false
	case d.Payload.Type != "token_count" || d.Payload.Info == nil:
		return Record{}, false
	}

	info := d.Payload.Info
	if total := info.Total.TotalTokens; total > 0 {
		if total == p.st.LastTotal {
			return Record{}, false
		}
		p.st.LastTotal = total
	}
	u := info.Last
	if u.Input == 0 && u.Output == 0 {
		return Record{}, false
	}
	ts, ok := parseTime(d.Timestamp)
	if !ok {
		return Record{}, false
	}

	// input_tokens includes cached_input_tokens; split them so cache reads are
	// neither counted twice nor billed at the full input price.
	input := max(u.Input-u.Cached, 0)
	model := p.st.Model
	if model == "" {
		model = unknownModel
	}
	return Record{
		// A fork copies events verbatim, timestamp and cumulative total included.
		Key: hashKey("codex", d.Timestamp,
			strconv.FormatInt(info.Total.TotalTokens, 10),
			strconv.FormatInt(u.Input, 10), strconv.FormatInt(u.Output, 10)),
		Time:        ts,
		Model:       model,
		RawProvider: p.st.RawProvider,
		Tokens:      Tokens{Input: input, CacheRead: u.Cached, Output: u.Output, Thinking: u.Reasoning},
	}, true
}
