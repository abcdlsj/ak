package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// sessionRecord is one line of sessions.jsonl, written by the SessionStart hook.
type sessionRecord struct {
	SessionID string    `json:"session_id"`
	Provider  string    `json:"provider"`
	TS        time.Time `json:"ts"`
}

// sessionIndex maps a claude session to the providers it ran under, oldest
// first. A resumed session can switch provider, so a session may have several.
type sessionIndex map[string][]sessionRecord

func sessionsPath() (string, error) {
	dir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions.jsonl"), nil
}

func loadSessionIndex() sessionIndex {
	idx := sessionIndex{}
	p, err := sessionsPath()
	if err != nil {
		return idx
	}
	f, err := os.Open(p)
	if err != nil {
		return idx
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r sessionRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.SessionID != "" && r.Provider != "" {
			idx[r.SessionID] = append(idx[r.SessionID], r)
		}
	}
	for _, rs := range idx {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].TS.Before(rs[j].TS) })
	}
	return idx
}

// owner returns the provider in effect for a session at time t: the latest
// record at or before t, else the earliest one.
func (idx sessionIndex) owner(session string, t time.Time) (string, bool) {
	rs := idx[session]
	if len(rs) == 0 {
		return "", false
	}
	best := rs[0]
	for _, r := range rs[1:] {
		if r.TS.After(t) {
			break
		}
		best = r
	}
	return best.Provider, true
}

// RecordSession appends a session attribution record; called by the hook.
func RecordSession(sessionID, provider string) error {
	if sessionID == "" || provider == "" {
		return nil
	}
	p, err := sessionsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(sessionRecord{SessionID: sessionID, Provider: provider, TS: time.Now().UTC()})
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// RenameProvider rewrites the session records of a renamed provider, so its
// claude usage keeps showing under the new name.
func RenameProvider(from, to string) error {
	p, err := sessionsPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := bytes.Split(data, []byte("\n"))
	changed := false
	for i, line := range lines {
		var r sessionRecord
		if json.Unmarshal(line, &r) != nil || r.Provider != from {
			continue
		}
		r.Provider = to
		if lines[i], err = json.Marshal(r); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return config.AtomicWrite(p, bytes.Join(lines, []byte("\n")), 0o600)
}
