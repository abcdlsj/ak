package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cache holds incremental scan state. Fully parsing hundreds of MB of logs
// takes seconds every time; this brings repeat runs down to near-instant.
type Cache struct {
	// Files records each file's fingerprint and parsed offset.
	Files map[string]fileFinger `json:"files"`
	// Rows holds each file's parsed results, reused directly when unchanged.
	Rows map[string][]Row `json:"rows"`
	// Version invalidates the whole cache when it changes.
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

// sessionIndex maps session_id to provider, maintained by the SessionStart hook.
type sessionIndex struct {
	Sessions map[string]string `json:"sessions"` // session_id -> provider
}

var sessionIdx sessionIndex

// loadSessionIndex loads the attribution index.
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

// lookupSessionOwner looks up the provider a session belongs to.
func lookupSessionOwner(id string) (string, bool) {
	if sessionIdx.Sessions == nil {
		loadSessionIndex()
	}
	p, ok := sessionIdx.Sessions[id]
	return p, ok
}

// RecordSession appends a session attribution record; called by the hook.
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
