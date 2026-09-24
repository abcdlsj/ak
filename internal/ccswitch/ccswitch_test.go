package ccswitch

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildDB creates a minimal database shaped like cc-switch's and returns its path.
func buildDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cc-switch.db")

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE providers (
			id TEXT NOT NULL, app_type TEXT NOT NULL, name TEXT NOT NULL,
			settings_config TEXT NOT NULL, meta TEXT NOT NULL DEFAULT '{}',
			sort_index INTEGER, is_current BOOLEAN NOT NULL DEFAULT 0,
			PRIMARY KEY (id, app_type))`,
		`CREATE TABLE provider_endpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT, provider_id TEXT NOT NULL,
			app_type TEXT NOT NULL, url TEXT NOT NULL, added_at INTEGER)`,
		`INSERT INTO providers VALUES ('a', 'claude', 'Claude One', '{"env":{"ANTHROPIC_BASE_URL":"https://one"}}', '{}', 1, 1)`,
		`INSERT INTO providers VALUES ('b', 'codex', 'Codex One', '{"config":"..."}', '{}', 1, 0)`,
		`INSERT INTO providers VALUES ('g', 'gemini', 'Gemini', '{}', '{}', 1, 0)`,
		`INSERT INTO provider_endpoints (provider_id, app_type, url, added_at) VALUES ('b', 'codex', 'https://ep1', 1)`,
		`INSERT INTO provider_endpoints (provider_id, app_type, url, added_at) VALUES ('b', 'codex', 'https://ep2', 2)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return path
}

func TestScanFile(t *testing.T) {
	raws, err := ScanFile(buildDB(t))
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if len(raws) != 3 {
		t.Fatalf("got %d rows, want 3", len(raws))
	}

	// Ordered by app_type then sort_index then name: claude, codex, gemini.
	claude := raws[0]
	if claude.ID != "a" || claude.AppType != "claude" || !claude.IsCurrent {
		t.Errorf("unexpected claude row: %+v", claude)
	}
	codex := raws[1]
	if codex.ID != "b" {
		t.Fatalf("expected codex second, got %+v", codex)
	}
	if got := codex.Endpoints; len(got) != 2 || got[0] != "https://ep1" || got[1] != "https://ep2" {
		t.Errorf("codex endpoints = %v, want [https://ep1 https://ep2]", got)
	}
	if len(claude.Endpoints) != 0 {
		t.Errorf("claude endpoints = %v, want none", claude.Endpoints)
	}
}

func TestScanFileMissing(t *testing.T) {
	if _, err := ScanFile(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("expected an error for a missing database")
	}
}

func TestScanFileReadOnly(t *testing.T) {
	path := buildDB(t)
	db, err := sql.Open("sqlite", readOnlyURI(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO providers VALUES ('x','claude','x','{}','{}',1,0)`); err == nil {
		t.Fatal("the read-only connection accepted a write")
	}
}
