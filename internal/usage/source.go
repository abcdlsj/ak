package usage

import (
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// Source is one engine's session logs. Adding an engine means adding a Source
// to sources; scanning, caching, dedupe and attribution are shared.
type Source interface {
	// Engine is the stable engine name stored in rows and the cache.
	Engine() string
	// Roots lists the directories holding this engine's *.jsonl logs.
	Roots(cfg *config.Config, home string) []string
	// NewParser returns a parser resuming from state, which is nil for a new
	// file and otherwise what a previous parser's State returned.
	NewParser(state []byte) LineParser
	// Provider attributes a bucket to an ak provider; "" means unknown.
	Provider(b bucket, a *attribution) string
}

// LineParser turns one complete jsonl line into a usage record.
type LineParser interface {
	Parse(line []byte) (Record, bool)
	// State is persisted so parsing can resume when the file grows.
	State() []byte
}

// Record is one request's usage as read from a log line.
type Record struct {
	// Key identifies the request for dedupe: resumed and forked sessions copy
	// history into new files, and a record seen elsewhere is skipped. Zero
	// disables dedupe.
	//
	// A record whose Key equals the one counted just before it supersedes it:
	// claude writes a message once per content block, each line carrying the
	// usage so far.
	Key         uint64
	Time        time.Time
	Model       string
	RawProvider string // engine-native provider id, if the log records one
	Session     string
	Tokens      Tokens
}

var sources = []Source{claudeSource{}, codexSource{}}

func sourceFor(engine string) Source {
	for _, s := range sources {
		if s.Engine() == engine {
			return s
		}
	}
	return nil
}
