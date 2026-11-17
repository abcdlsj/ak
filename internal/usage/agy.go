package usage

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// AgyActivity is what an antigravity provider's own store says it did. agy
// keeps no token counts anywhere — a conversation's steps are content, and the
// summary row records how many there were — so a conversation and the steps in
// it are all there is to count, and none of it can be priced.
type AgyActivity struct {
	Provider      string `json:"provider"`
	Conversations int    `json:"conversations"`
	Steps         int64  `json:"steps"`
	// Last is when the provider was last used, zero when nothing is recorded.
	Last time.Time `json:"last,omitzero"`
}

// AgyActivities counts, for every antigravity provider in cfg, the
// conversations last used since f.Since and the steps agy recorded for them.
//
// The store is agy's own and one per provider, because the directory is the
// account. It is read read-only, and anything unreadable — no directory, no
// file, a schema a later agy changed — counts as nothing recorded rather than
// an error: agy's files are not ak's to depend on.
func AgyActivities(cfg *config.Config, f Filter) []AgyActivity {
	var out []AgyActivity
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.Kind != config.KindAntigravity || p.GeminiDir == "" {
			continue
		}
		if f.Provider != "" && f.Provider != name {
			continue
		}
		out = append(out, agyActivity(name, config.ExpandHome(p.GeminiDir), f.Since))
	}
	return out
}

// agySummaries is where agy keeps one row per conversation, its step count and
// when it was last used.
func agySummaries(geminiDir string) string {
	return filepath.Join(geminiDir, provider.AntigravityAppDataDir, "conversation_summaries.db")
}

func agyActivity(name, geminiDir, since string) AgyActivity {
	a := AgyActivity{Provider: name}
	path := agySummaries(geminiDir)
	if _, err := os.Stat(path); err != nil {
		return a
	}
	// Read-only, and without creating anything: the file belongs to agy, which
	// may be running.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return a
	}
	defer db.Close()
	rows, err := db.Query(`select step_count, last_user_input_time, last_modified_time
		from conversation_summaries`)
	if err != nil {
		return a
	}
	defer rows.Close()
	for rows.Next() {
		var (
			steps           int64
			input, modified string
		)
		if err := rows.Scan(&steps, &input, &modified); err != nil {
			break
		}
		// A conversation is counted on the day it was last used: agy records no
		// per-step time, so a run stretched over days cannot be split.
		at := parseAgyTime(input)
		if at.IsZero() {
			at = parseAgyTime(modified)
		}
		if at.IsZero() {
			continue
		}
		if since != "" && at.Local().Format(dateLayout) < since {
			continue
		}
		a.Conversations++
		a.Steps += steps
		if at.After(a.Last) {
			a.Last = at
		}
	}
	return a
}

// parseAgyTime reads the timestamps agy writes into its summaries: a sqlite
// datetime with or without fractional seconds, UTC or with an offset.
func parseAgyTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
		time.RFC3339,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
