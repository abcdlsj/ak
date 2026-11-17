package usage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// agyStore writes a conversation store the shape agy keeps, where a provider's
// own directory would hold it.
func agyStore(t *testing.T, rows ...[3]string) string {
	t.Helper()
	dir := t.TempDir()
	path := agySummaries(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Only the columns ak reads, with agy's own types.
	if _, err := db.Exec(`create table conversation_summaries (
		conversation_id text primary key, title text, step_count integer,
		last_modified_time datetime, last_user_input_time datetime)`); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if _, err := db.Exec(`insert into conversation_summaries
			(conversation_id, step_count, last_user_input_time, last_modified_time) values (?, ?, ?, ?)`,
			string(rune('a'+i)), r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func agyCfg(dir string) *config.Config {
	cfg := config.Default()
	cfg.Providers["work"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: dir}
	return cfg
}

// A provider's activity is the conversations last used in the window, with the
// steps agy recorded for them.
func TestAgyActivitiesCountsWhatAgyRecorded(t *testing.T) {
	dir := agyStore(t,
		[3]string{"12", "2026-11-17 03:51:50.873132+00:00", "2026-11-17 03:51:50.873132+00:00"},
		[3]string{"4", "2026-11-10 08:00:00+00:00", "2026-11-10 08:00:00+00:00"},
		// No user input time: the modified time answers.
		[3]string{"2", "", "2026-11-16 09:00:00+00:00"},
	)
	got := AgyActivities(agyCfg(dir), Filter{})
	if len(got) != 1 || got[0].Provider != "work" {
		t.Fatalf("activities = %+v", got)
	}
	if got[0].Conversations != 3 || got[0].Steps != 18 {
		t.Errorf("conversations/steps = %d/%d, want 3/18", got[0].Conversations, got[0].Steps)
	}
	if want := time.Date(2026, 11, 17, 3, 51, 50, 873132000, time.UTC); !got[0].Last.Equal(want) {
		t.Errorf("last = %v, want %v", got[0].Last, want)
	}

	// A window keeps only what was used inside it.
	got = AgyActivities(agyCfg(dir), Filter{Since: "2026-11-16"})
	if got[0].Conversations != 2 || got[0].Steps != 14 {
		t.Errorf("in window = %+v, want the two from the 16th and the 17th", got[0])
	}

	// A filter on another provider leaves the antigravity ones out.
	if got := AgyActivities(agyCfg(dir), Filter{Provider: "other"}); len(got) != 0 {
		t.Errorf("filtered = %+v", got)
	}
}

// Nothing recorded is not an error: no directory, no file, and a file agy
// writes differently in a later release all answer zero.
func TestAgyActivitiesWithoutAStore(t *testing.T) {
	missing := agyCfg(t.TempDir())
	if got := AgyActivities(missing, Filter{}); len(got) != 1 || got[0].Conversations != 0 || got[0].Steps != 0 {
		t.Errorf("with no store = %+v", got)
	}
	if !got0LastZero(t, missing) {
		t.Error("with no store the last use was not zero")
	}

	junk := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(agySummaries(junk)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agySummaries(junk), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := AgyActivities(agyCfg(junk), Filter{}); len(got) != 1 || got[0].Conversations != 0 {
		t.Errorf("with an unreadable store = %+v", got)
	}

	// A store whose schema moved on answers nothing rather than a wrong number.
	moved := agyStore(t, [3]string{"9", "2026-11-17 00:00:00+00:00", "2026-11-17 00:00:00+00:00"})
	db, err := sql.Open("sqlite", "file:"+agySummaries(moved))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`alter table conversation_summaries add column extra text`); err != nil {
		t.Fatal(err)
	}
	if got := AgyActivities(agyCfg(moved), Filter{}); len(got) != 1 || got[0].Conversations != 1 {
		t.Errorf("with an extra column = %+v", got)
	}
}

func got0LastZero(t *testing.T, cfg *config.Config) bool {
	t.Helper()
	got := AgyActivities(cfg, Filter{})
	return len(got) == 1 && got[0].Last.IsZero()
}

// Only antigravity providers are read, and only their own directory.
func TestAgyActivitiesSkipsOtherEngines(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["claude"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a"}
	cfg.Providers["no-dir"] = config.Provider{Kind: config.KindAntigravity}
	if got := AgyActivities(cfg, Filter{}); len(got) != 0 {
		t.Errorf("activities = %+v", got)
	}
}

// The store lives where the provider's agy keeps it, which is what makes one
// directory one account.
func TestAgySummariesPath(t *testing.T) {
	want := filepath.Join("/tmp/x", provider.AntigravityAppDataDir, "conversation_summaries.db")
	if got := agySummaries("/tmp/x"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
