package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cache 是增量扫描的状态。数百 MB 的日志每次全解要数秒,靠它做到近乎瞬时。
type Cache struct {
	// Files 记录每个文件的指纹与已解析偏移。
	Files map[string]fileFinger `json:"files"`
	// Rows 是每个文件的已解析结果,文件未变时直接复用。
	Rows map[string][]Row `json:"rows"`
	// Version 变化时缓存整体作废。
	Version int `json:"version"`
}

const cacheVersion = 1

func newCache() Cache {
	return Cache{
		Files:   map[string]fileFinger{},
		Rows:    map[string][]Row{},
		Version: cacheVersion,
	}
}

func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "ak")
	return filepath.Join(dir, "usage-cache.json"), nil
}

func loadCache() (Cache, error) {
	p, err := cachePath()
	if err != nil {
		return newCache(), err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return newCache(), err
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return newCache(), err
	}
	if c.Version != cacheVersion || c.Files == nil || c.Rows == nil {
		return newCache(), nil
	}
	return c, nil
}

func saveCache(c Cache) error {
	p, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// sessionIndex 是 session_id → 供应商的映射,由 SessionStart hook 维护。
type sessionIndex struct {
	Sessions map[string]string `json:"sessions"` // session_id -> provider
}

var sessionIdx sessionIndex

// loadSessionIndex 载入归属索引。
func loadSessionIndex() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	p := filepath.Join(home, ".local", "share", "ak", "sessions.jsonl")
	data, err := os.ReadFile(p)
	if err != nil {
		sessionIdx.Sessions = map[string]string{}
		return
	}
	sessionIdx.Sessions = map[string]string{}
	for _, line := range splitLines(data) {
		var e struct {
			SessionID string `json:"session_id"`
			Provider  string `json:"provider"`
		}
		if json.Unmarshal(line, &e) == nil && e.SessionID != "" && e.Provider != "" {
			sessionIdx.Sessions[e.SessionID] = e.Provider
		}
	}
}

// lookupSessionOwner 查 session 归属的供应商。
func lookupSessionOwner(id string) (string, bool) {
	if sessionIdx.Sessions == nil {
		loadSessionIndex()
	}
	p, ok := sessionIdx.Sessions[id]
	return p, ok
}

// RecordSession 追加一条 session 归属记录,hook 调用。
func RecordSession(sessionID, provider string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".local", "share", "ak")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "sessions.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	rec := map[string]string{
		"session_id": sessionID,
		"provider":   provider,
		"ts":         time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
