// Package usage counts token usage for claude and codex.
//
// The data source is local session logs, not proxy forwarding: cc-switch
// accounts for usage through a self-hosted proxy. ak runs no proxy, so it
// parses the jsonl files under ~/.claude/projects and ~/.codex/sessions.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// Tokens is the token breakdown of a single request.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Thinking   int64 `json:"thinking"`
}

// Total is the billable amount: cache reads and writes are counted separately,
// matching models.dev's billing basis.
func (t Tokens) Total() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

// Row is one aggregation result.
type Row struct {
	Date     string `json:"date"`     // YYYY-MM-DD
	Provider string `json:"provider"` // Provider name; unknown means unattributable
	Model    string `json:"model"`
	Engine   string `json:"engine"` // claude | codex
	Tokens   Tokens `json:"tokens"`
	Sessions int    `json:"sessions"`
}

// Summary is the aggregated view.
type Summary struct {
	TotalTokens int64         `json:"total_tokens"`
	TotalCost   float64       `json:"total_cost"`
	Rows        []Row         `json:"rows"`
	ByProvider  []ProviderRow `json:"by_provider"`
	ByModel     []ModelRow    `json:"by_model"`
	ByHour      [24]int64     `json:"by_hour"`
	// ByDate is the heatmap data source, ascending by date.
	ByDate []DateRow `json:"by_date"`
	// UnpricedTokens is the token count with no pricing data, so the cost figure is an underestimate.
	UnpricedTokens int64 `json:"unpriced_tokens"`
}

// ProviderRow aggregates by provider.
type ProviderRow struct {
	Name   string  `json:"name"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// ModelRow aggregates by model.
type ModelRow struct {
	Model  string  `json:"model"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// DateRow aggregates by date, used by the heatmap.
type DateRow struct {
	Date   string  `json:"date"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// Aggregate counts everything. The first scan parses hundreds of MB of logs;
// subsequent runs are incremental via the cache.
func Aggregate(cfg *config.Config) (Summary, error) {
	cache, err := loadCache()
	if err != nil {
		cache = newCache()
	}

	rows, err := scanAll(cfg, &cache)
	if err != nil {
		return Summary{}, err
	}
	if err := saveCache(cache); err != nil {
		// A cache write failure does not affect the result.
		_ = err
	}

	return summarize(rows, cfg), nil
}

// summarize aggregates detail rows into the various views.
func summarize(rows []Row, cfg *config.Config) Summary {
	var s Summary
	byProvider := map[string]*ProviderRow{}
	byModel := map[string]*ModelRow{}
	byDate := map[string]*DateRow{}

	for _, r := range rows {
		t := r.Tokens
		s.TotalTokens += t.Total()
		cost, priced := costOf(r, cfg)
		if priced {
			s.TotalCost += cost
		} else {
			s.UnpricedTokens += t.Total()
		}

		pr := byProvider[r.Provider]
		if pr == nil {
			pr = &ProviderRow{Name: r.Provider}
			byProvider[r.Provider] = pr
		}
		pr.Tokens += t.Total()
		pr.Cost += cost

		mr := byModel[r.Model]
		if mr == nil {
			mr = &ModelRow{Model: r.Model}
			byModel[r.Model] = mr
		}
		mr.Tokens += t.Total()
		mr.Cost += cost

		dr := byDate[r.Date]
		if dr == nil {
			dr = &DateRow{Date: r.Date}
			byDate[r.Date] = dr
		}
		dr.Tokens += t.Total()
		dr.Cost += cost
	}

	s.Rows = rows
	s.ByProvider = sortedProviders(byProvider)
	s.ByModel = sortedModels(byModel)
	s.ByDate = sortedDates(byDate)
	return s
}

func sortedProviders(m map[string]*ProviderRow) []ProviderRow {
	out := make([]ProviderRow, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens > out[j].Tokens })
	return out
}

func sortedModels(m map[string]*ModelRow) []ModelRow {
	out := make([]ModelRow, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens > out[j].Tokens })
	return out
}

func sortedDates(m map[string]*DateRow) []DateRow {
	out := make([]DateRow, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// ---- scanning ----

// fileFinger is a file's incremental fingerprint. The file is re-parsed only
// when size or mtime changes.
type fileFinger struct {
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
	// Offset is the byte position already parsed, enabling new-bytes-only parsing.
	Offset int64 `json:"offset"`
}

// scanAll scans the claude and codex log directories.
func scanAll(cfg *config.Config, cache *Cache) ([]Row, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	var rows []Row

	claudeRoot := filepath.Join(home, ".claude", "projects")
	r, err := scanTree(claudeRoot, "claude", cache, parseClaudeLine)
	if err != nil {
		return nil, err
	}
	rows = append(rows, r...)

	codexRoot := filepath.Join(home, ".codex", "sessions")
	r, err = scanTree(codexRoot, "codex", cache, parseCodexLine)
	if err != nil {
		return nil, err
	}
	rows = append(rows, r...)

	return rows, nil
}

// lineParser extracts usage from one jsonl line; false means the line is irrelevant.
type lineParser func(line []byte, row *Row) bool

// scanTree recursively scans *.jsonl under a directory.
func scanTree(root, engine string, cache *Cache, parse lineParser) ([]Row, error) {
	var rows []Row

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		fp := fileFinger{Size: fi.Size(), Mtime: fi.ModTime()}

		cached, ok := cache.Files[path]
		if ok && cached.Size == fp.Size && cached.Mtime.Equal(fp.Mtime) {
			// Unchanged: reuse the cached rows directly.
			rows = append(rows, cache.Rows[path]...)
			return nil
		}

		var fileRows []Row
		offset := int64(0)
		if ok && cached.Size <= fp.Size && cached.Mtime.Equal(fp.Mtime) {
			offset = cached.Offset
		}
		fileRows, newOffset, err := parseFile(path, engine, offset, parse)
		if err != nil {
			return nil
		}
		fp.Offset = newOffset
		cache.Files[path] = fp
		cache.Rows[path] = fileRows
		rows = append(rows, fileRows...)
		return nil
	})

	return rows, nil
}

// parseFile parses the new portion of a single jsonl.
// sessionMeta holds the metadata of the session owning this file
// (absent on the claude side, present on the codex side).
func parseFile(path, engine string, offset int64, parse lineParser) ([]Row, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}

	// codex's session_meta carries model_provider and model, neither of which
	// appears in token_count events, so both must be filled in from here.
	meta := sessionMeta{}
	if engine == "codex" && offset == 0 {
		meta = codexSessionMeta(f)
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}

	var rows []Row
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			// Pre-filter: lines without usage / token_count are not worth a JSON decode.
			// The vast majority of the hundreds of MB of logs are plain text, so
			// this step saves most of the work.
			if bytes.Contains(line, []byte(`"usage"`)) ||
				bytes.Contains(line, []byte(`"token_count"`)) {
				var row Row
				if parse(line, &row) {
					if row.Provider == unknownProvider && meta.Provider != "" {
						row.Provider = meta.Provider
					}
					if row.Model == unknownModel && meta.Model != "" {
						row.Model = meta.Model
					}
					rows = append(rows, row)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return rows, fiSizeOr(f, offset), nil
}

// sessionMeta is the metadata of a codex session file.
type sessionMeta struct {
	Provider string
	Model    string
}

// codexSessionMeta reads a codex session file for model_provider and model.
// They appear in different events: model_provider in the first line's
// session_meta, model in turn_context. Neither is present in token_count
// events, so both must be filled in from here.
func codexSessionMeta(f *os.File) sessionMeta {
	out := sessionMeta{}
	br := bufio.NewReader(f)
	for i := 0; i < 50; i++ {
		line, err := br.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"session_meta"`)) {
			var d struct {
				Payload struct {
					ModelProvider string `json:"model_provider"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &d) == nil && d.Payload.ModelProvider != "" {
				out.Provider = d.Payload.ModelProvider
			}
		}
		if bytes.Contains(line, []byte(`"turn_context"`)) {
			var d struct {
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &d) == nil && d.Payload.Model != "" {
				out.Model = d.Payload.Model
			}
		}
		if err != nil || (out.Provider != "" && out.Model != "") {
			break
		}
	}
	return out
}

// dateOf takes the date portion of an ISO timestamp.
func dateOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return "unknown"
}

// unknownProvider / unknownModel are placeholders for unknown attribution or model.
const (
	unknownProvider = "unknown"
	unknownModel    = "unknown"
)

// parseClaudeLine parses claude's assistant messages.
// Each carries full usage: input/output/cache_creation/cache_read/thinking.
func parseClaudeLine(line []byte, row *Row) bool {
	var d struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		SessionID string `json:"sessionId"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				Input               int64 `json:"input_tokens"`
				Output              int64 `json:"output_tokens"`
				CacheCreation       int64 `json:"cache_creation_input_tokens"`
				CacheRead           int64 `json:"cache_read_input_tokens"`
				OutputTokensDetails struct {
					Thinking int64 `json:"thinking_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &d); err != nil {
		return false
	}
	if d.Type != "assistant" || d.Message.Model == "" {
		return false
	}

	row.Engine = "claude"
	row.Model = d.Message.Model
	// claude's jsonl records no provider; look it up in the attribution index
	// maintained by the SessionStart hook.
	row.Provider = providerForSession(d.SessionID)
	row.Date = dateOf(d.Timestamp)
	row.Sessions = 1
	row.Tokens = Tokens{
		Input:      d.Message.Usage.Input,
		Output:     d.Message.Usage.Output,
		CacheWrite: d.Message.Usage.CacheCreation,
		CacheRead:  d.Message.Usage.CacheRead,
		Thinking:   d.Message.Usage.OutputTokensDetails.Thinking,
	}
	return true
}

// providerForSession looks up the provider from ak's own session attribution index.
func providerForSession(sessionID string) string {
	if sessionID == "" {
		return unknownProvider
	}
	if p, ok := lookupSessionOwner(sessionID); ok {
		return p
	}
	return unknownProvider
}

// fiSizeOr takes the file's current size, falling back to the default on error.
func fiSizeOr(f *os.File, def int64) int64 {
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return def
}

// parseCodexLine parses codex's token_count events.
//
// Note: total_token_usage is a CUMULATIVE value, valid only as the session's
// last reading; last_token_usage is the increment and may be summed per event.
// Mixing the two double-counts, and this is the easiest mistake to make in this
// package. Neither model nor model_provider appears in these events; the caller
// fills them from session_meta / turn_context.
func parseCodexLine(line []byte, row *Row) bool {
	var d struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type string `json:"type"`
			Info struct {
				LastTokenUsage struct {
					Input        int64 `json:"input_tokens"`
					Output       int64 `json:"output_tokens"`
					CacheRead    int64 `json:"cached_input_tokens"`
					CacheWrite   int64 `json:"cache_write_input_tokens"`
					ReasoningOut int64 `json:"reasoning_output_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &d); err != nil {
		return false
	}
	if d.Payload.Type != "token_count" {
		return false
	}
	u := d.Payload.Info.LastTokenUsage
	if u.Input == 0 && u.Output == 0 {
		return false
	}

	row.Engine = "codex"
	row.Model = unknownModel
	row.Provider = unknownProvider
	row.Date = dateOf(d.Timestamp)
	row.Sessions = 1
	row.Tokens = Tokens{
		Input:      u.Input,
		Output:     u.Output,
		CacheRead:  u.CacheRead,
		CacheWrite: u.CacheWrite,
		Thinking:   u.ReasoningOut,
	}
	return true
}
