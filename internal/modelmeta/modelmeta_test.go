package modelmeta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// cacheFrom writes a table where ak keeps its cache and points the package
// there, so each test answers from its own.
func cacheFrom(t *testing.T, facts map[string]Facts) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	data, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model-meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWindow(t *testing.T) {
	tests := []struct {
		name string
		in   limit
		want int
	}{
		{"context alone", limit{Context: 200000}, 200000},
		{"input limit wins", limit{Context: 400000, Input: 272000}, 272000},
		{"window narrower than the input limit", limit{Context: 128000, Input: 272000}, 128000},
		{"nothing said", limit{}, 0},
		{"nonsense ignored", limit{Context: 512}, 0},
		{"one side nonsense", limit{Context: 512, Input: 128000}, 128000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := window(tc.in); got != tc.want {
				t.Errorf("window(%+v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestFactsOfOutput(t *testing.T) {
	if got := factsOf(&limit{Context: 200000, Output: 64000}); got.MaxOutput != 64000 {
		t.Errorf("output = %d", got.MaxOutput)
	}
	if got := factsOf(&limit{Context: 200000, Output: 100}); got.MaxOutput != 0 {
		t.Errorf("a reply limit under the floor was taken: %d", got.MaxOutput)
	}
	if got := factsOf(nil); got != (Facts{}) {
		t.Errorf("no limit block gave %+v", got)
	}
}

func TestBuildTable(t *testing.T) {
	d := doc{
		"openai": {Models: map[string]struct {
			Limit *limit `json:"limit"`
		}{
			"gpt-5": {Limit: &limit{Context: 400000, Input: 272000, Output: 128000}},
			"big":   {Limit: &limit{Context: 1000000}},
		}},
		"a-relay": {Models: map[string]struct {
			Limit *limit `json:"limit"`
		}{
			// The same id, listed first by name, wins.
			"gpt-5": {Limit: &limit{Context: 200000}},
			// Nothing said, so no row: the engine keeps its own default.
			"quiet": {Limit: nil},
		}},
	}
	table := buildTable(d)
	if got := table["gpt-5"]; got != (Facts{Context: 200000}) {
		t.Errorf("gpt-5 = %+v, want the first provider by name", got)
	}
	if got := table["big"]; got != (Facts{Context: 1000000}) {
		t.Errorf("big = %+v", got)
	}
	if _, ok := table["quiet"]; ok {
		t.Error("a model with no limits got a row")
	}
}

func TestCandidates(t *testing.T) {
	// The order is the one prices were always looked up in: the id, then the id
	// without its vendor, then the same lowercased, then each without the
	// suffixes relays add.
	want := []string{
		"Moonshot/Kimi-K3-Preview", "Kimi-K3-Preview",
		"moonshot/kimi-k3-preview", "kimi-k3-preview",
		"moonshot/kimi-k3", "kimi-k3",
	}
	got := Candidates("Moonshot/Kimi-K3-Preview")
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
}

func TestForPrefersTheProvider(t *testing.T) {
	cacheFrom(t, map[string]Facts{
		"m":      {Context: 200000, MaxOutput: 64000},
		"vendor": {Context: 1000000},
	})
	p := config.Provider{Model: "m"}

	if got := For(p, "m"); got != (Facts{Context: 200000, MaxOutput: 64000}) {
		t.Errorf("a table row was not used: %+v", got)
	}
	if got := For(p, "vendor/m"); got != (Facts{Context: 200000, MaxOutput: 64000}) {
		t.Errorf("the vendor prefix was not looked through: %+v", got)
	}
	if got := For(p, "unknown"); got != (Facts{}) {
		t.Errorf("an unknown model answered %+v", got)
	}

	// A provider's own numbers win over the table, and fill only what it leaves
	// blank.
	p.ContextWindow = 500000
	if got := For(p, "m"); got != (Facts{Context: 500000, MaxOutput: 64000}) {
		t.Errorf("provider numbers = %+v", got)
	}
	p.MaxTokens = 32000
	if got := For(p, "m"); got != (Facts{Context: 500000, MaxOutput: 32000}) {
		t.Errorf("provider numbers = %+v", got)
	}
	// And they answer for a model the table has never heard of.
	if got := For(p, "unknown"); got != (Facts{Context: 500000, MaxOutput: 32000}) {
		t.Errorf("provider numbers for an unknown model = %+v", got)
	}
}

func TestForColdCache(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	p := config.Provider{ContextWindow: 131072}
	if got := For(p, "m"); got != (Facts{Context: 131072}) {
		t.Errorf("with no cache = %+v", got)
	}
}

func TestRefreshKeepsAFreshCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	path := filepath.Join(dir, "model-meta.json")
	if err := writeTable(path, map[string]Facts{"a": {Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	// A cache younger than the TTL is taken as it is: Refresh must not fetch,
	// which it would here, with no network in a test.
	if err := Refresh(); err != nil {
		t.Fatalf("Refresh on a fresh cache: %v", err)
	}
	if got := For(config.Provider{}, "a"); got != (Facts{Context: 1000000}) {
		t.Errorf("after Refresh = %+v", got)
	}
}

func TestReadFreshHonoursTheTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-meta.json")
	if err := writeTable(path, map[string]Facts{"a": {Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readFresh(path); !ok {
		t.Error("a cache just written was taken for stale")
	}
	old := time.Now().Add(-2 * ttl)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if t2, ok := readFresh(path); ok {
		t.Errorf("a cache two days old was taken for fresh: %v", t2)
	}
	// However old, it still answers: an old window beats none.
	if got := readTable(path); len(got) != 1 {
		t.Errorf("a stale cache was not readable: %v", got)
	}
}
