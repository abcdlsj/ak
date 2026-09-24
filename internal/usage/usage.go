// Package usage 统计 claude 与 codex 的 token 用量。
//
// 数据源是本机 session 日志,不是代理转发:cc-switch 靠自建代理记账,
// ak 不挂代理,所以改为解析 ~/.claude/projects 与 ~/.codex/sessions 的 jsonl。
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

// Tokens 是一次请求的 token 明细。
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Thinking   int64 `json:"thinking"`
}

// Total 是计费用的总量:cache 读写单列不计入,与 models.dev 的计费口径一致。
func (t Tokens) Total() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

// Row 是一条聚合结果。
type Row struct {
	Date     string `json:"date"`     // YYYY-MM-DD
	Provider string `json:"provider"` // 供应商名,unknown 表示无法归属
	Model    string `json:"model"`
	Engine   string `json:"engine"` // claude | codex
	Tokens   Tokens `json:"tokens"`
	Sessions int    `json:"sessions"`
}

// Summary 是汇总视图。
type Summary struct {
	TotalTokens int64         `json:"total_tokens"`
	TotalCost   float64       `json:"total_cost"`
	Rows        []Row         `json:"rows"`
	ByProvider  []ProviderRow `json:"by_provider"`
	ByModel     []ModelRow    `json:"by_model"`
	ByHour      [24]int64     `json:"by_hour"`
	// ByDate 是热力图的数据源,按日期升序。
	ByDate []DateRow `json:"by_date"`
	// UnpricedTokens 是查不到定价的 token 数,成本数字会偏低。
	UnpricedTokens int64 `json:"unpriced_tokens"`
}

// ProviderRow 是按供应商汇总。
type ProviderRow struct {
	Name   string  `json:"name"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// ModelRow 是按模型汇总。
type ModelRow struct {
	Model  string  `json:"model"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// DateRow 是按日期汇总,热力图用。
type DateRow struct {
	Date   string  `json:"date"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// Aggregate 全量统计。首次扫描需要解析数百 MB 日志,之后靠缓存增量。
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
		// 缓存写失败不影响结果。
		_ = err
	}

	return summarize(rows, cfg), nil
}

// summarize 把明细行汇总成各种视图。
func summarize(rows []Row, cfg *config.Config) Summary {
	var s Summary
	byProvider := map[string]*ProviderRow{}
	byModel := map[string]*ModelRow{}
	byDate := map[string]*DateRow{}

	for _, r := range rows {
		t := r.Tokens
		s.TotalTokens += t.Total()
		for h := 0; h < 24; h++ {
			// ByHour 由调用方按时间戳填,这里先留空。
		}

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

// ---- 扫描 ----

// fileFinger 是文件的增量指纹。size+mtime 变化才重新解析。
type fileFinger struct {
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
	// Offset 是已解析到的字节位置,支持只解析新增部分。
	Offset int64 `json:"offset"`
}

// scanAll 扫描 claude 与 codex 的日志目录。
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

// lineParser 从一行 jsonl 提取用量,返回 false 表示该行无关。
type lineParser func(line []byte, row *Row) bool

// scanTree 递归扫描目录下的 *.jsonl。
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
			// 未变,直接用缓存行。
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

// parseFile 解析单个 jsonl 的新增部分。
// sessionMeta 是该文件所属 session 的元信息(claude 侧无,codex 侧有)。
func parseFile(path, engine string, offset int64, parse lineParser) ([]Row, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}

	// codex 的 session_meta 在第一行,含 model_provider;先读出来供每行归属。
	meta := ""
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
			// 预筛:不含 usage / token_count 的行不值得做 JSON 解码。
			// 数百 MB 的日志里绝大多数行是纯文本,这一步省掉大部分开销。
			if bytes.Contains(line, []byte(`"usage"`)) ||
				bytes.Contains(line, []byte(`"token_count"`)) {
				var row Row
				if parse(line, &row) {
					if meta != "" && row.Provider == unknownProvider {
						row.Provider = meta
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

// codexSessionMeta 读 codex session 文件首行的 model_provider。
func codexSessionMeta(f *os.File) string {
	br := bufio.NewReader(f)
	for i := 0; i < 5; i++ {
		line, err := br.ReadBytes('\n')
		if !bytes.Contains(line, []byte(`"session_meta"`)) && !bytes.Contains(line, []byte(`"model_provider"`)) {
			if err != nil {
				break
			}
			continue
		}
		var d struct {
			Payload struct {
				ModelProvider string `json:"model_provider"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &d) == nil && d.Payload.ModelProvider != "" {
			return d.Payload.ModelProvider
		}
		if err != nil {
			break
		}
	}
	return ""
}

func fiSizeOr(f *os.File, def int64) int64 {
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return def
}

// parseClaudeLine 解析 claude 的 assistant 消息。
// 每条带完整 usage:input/output/cache_creation/cache_read/thinking。
func parseClaudeLine(line []byte, row *Row) bool {
	var d struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
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

// parseCodexLine 解析 codex 的 token_count 事件。
//
// 注意:total_token_usage 是**累计值**,只能取 session 的末值;
// last_token_usage 才是增量,可逐条累加。二者混用会算重 —— 这是本包最容易出错的地方。
func parseCodexLine(line []byte, row *Row) bool {
	var d struct {
		Type      string `json:"type"`
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
				Model string `json:"model"`
			} `json:"info"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &d); err != nil {
		return false
	}
	if d.Type != "event_msg" || d.Payload.Type != "token_count" {
		return false
	}
	u := d.Payload.Info.LastTokenUsage
	if u.Input == 0 && u.Output == 0 {
		return false
	}

	row.Engine = "codex"
	row.Model = d.Payload.Info.Model
	// 供应商由调用方用 session_meta 里的 model_provider 填。
	row.Provider = unknownProvider
	row.Date = dateOf(d.Timestamp)
	row.Sessions = 1
	row.Tokens = Tokens{
		Input:     u.Input,
		Output:    u.Output,
		CacheRead: u.CacheRead,
		Thinking:  u.ReasoningOut,
	}
	return true
}

// providerForSession 从 ak 自己的 session 归属索引查供应商。
// claude 的 jsonl 不记录供应商,靠 SessionStart hook 补。
func providerForSession(sessionID string) string {
	if sessionID == "" {
		return unknownProvider
	}
	if p, ok := lookupSessionOwner(sessionID); ok {
		return p
	}
	return unknownProvider
}

const unknownProvider = "unknown"

// dateOf 从 ISO 时间戳取日期部分。
func dateOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return "unknown"
}
