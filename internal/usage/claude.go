package usage

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// claudeSource reads ~/.claude/projects/**/*.jsonl.
type claudeSource struct{}

func (claudeSource) Engine() string { return "claude" }

func (claudeSource) Roots(cfg *config.Config, home string) []string {
	roots := []string{filepath.Join(home, ".claude", "projects")}
	for _, name := range cfg.Names() {
		if d := cfg.Providers[name].ConfigDir; d != "" {
			roots = append(roots, filepath.Join(config.ExpandHome(d), "projects"))
		}
	}
	return roots
}

// Provider looks the session up in the index the SessionStart hook maintains:
// claude's logs record no provider.
func (claudeSource) Provider(b bucket, a *attribution) string {
	p, _ := a.sessions.owner(b.Session, b.Last)
	return p
}

func (claudeSource) NewParser([]byte) LineParser { return claudeParser{} }

// claudeParser is stateless: every assistant line is self-contained.
type claudeParser struct{}

func (claudeParser) State() []byte { return nil }

func (claudeParser) Parse(line []byte) (Record, bool) {
	// Pre-filter: the vast majority of the log is plain text, not worth a JSON decode.
	if !bytes.Contains(line, []byte(`"usage"`)) || !bytes.Contains(line, []byte(`"assistant"`)) {
		return Record{}, false
	}
	var d struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		SessionID string `json:"sessionId"`
		Message   struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				Input         int64 `json:"input_tokens"`
				Output        int64 `json:"output_tokens"`
				CacheCreation int64 `json:"cache_creation_input_tokens"`
				CacheRead     int64 `json:"cache_read_input_tokens"`
				OutputDetails struct {
					Thinking int64 `json:"thinking_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &d) != nil || d.Type != "assistant" {
		return Record{}, false
	}
	// claude writes placeholder messages (model "<synthetic>") for local
	// errors and interrupts; they never reached a provider.
	if d.Message.Model == "" || strings.HasPrefix(d.Message.Model, "<") {
		return Record{}, false
	}
	ts, ok := parseTime(d.Timestamp)
	if !ok {
		return Record{}, false
	}
	u := d.Message.Usage
	r := Record{
		Time:    ts,
		Model:   d.Message.Model,
		Session: d.SessionID,
		Tokens: Tokens{
			Input:      u.Input,
			Output:     u.Output,
			CacheWrite: u.CacheCreation,
			CacheRead:  u.CacheRead,
			Thinking:   u.OutputDetails.Thinking,
		},
	}
	if d.Message.ID != "" {
		r.Key = hashKey("claude", d.Message.ID)
	}
	return r, r.Tokens.Total() > 0
}
