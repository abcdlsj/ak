package modelmeta

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// cacheFrom writes a catalog where ak keeps its cache and points the package
// there, so each test answers from its own.
func cacheFrom(t *testing.T, c catalog) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	if err := writeTable(filepath.Join(dir, cacheName), c); err != nil {
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
		"openai": {Models: map[string]row{
			"gpt-5": {Limit: &limit{Context: 400000, Input: 272000, Output: 128000}},
			"big":   {Limit: &limit{Context: 1000000}},
		}},
		"a-relay": {Models: map[string]row{
			// The same id, listed by a relay, does not beat the vendor's.
			"gpt-5": {Limit: &limit{Context: 200000}},
			// Nothing said, so no row: the engine keeps its own default.
			"quiet": {Limit: nil},
		}},
	}
	c := buildTable(d)
	if got := c.Models["gpt-5"]; got != (Facts{Context: 272000, MaxOutput: 128000}) {
		t.Errorf("gpt-5 = %+v, want the vendor's", got)
	}
	if got := c.Models["big"]; got != (Facts{Context: 1000000}) {
		t.Errorf("big = %+v", got)
	}
	if _, ok := c.Models["quiet"]; ok {
		t.Error("a model with no limits got a row")
	}
}

// A relay's numbers must never beat the vendor's: it rounds a window up to 1M,
// or states a reply limit as long as the window, and the engine that believes it
// fails on the first long request.
func TestBuildTablePrefersFirstParty(t *testing.T) {
	one, two := 1.0, 2.0
	d := doc{
		"aaa-relay": {Models: map[string]row{
			"gemini-x": {Limit: &limit{Context: 1048576, Output: 393216}, Cost: &cost{Input: two}},
		}},
		"google": {Models: map[string]row{
			"gemini-x": {Limit: &limit{Context: 131072}, Cost: &cost{Input: one}},
		}},
	}
	c := buildTable(d)
	if got := c.Models["gemini-x"]; got != (Facts{Context: 131072}) {
		t.Errorf("limits = %+v, want the vendor's", got)
	}
	if got := c.Prices["gemini-x"].Input; got != 1 {
		t.Errorf("price = %v, want the vendor's", got)
	}
}

// The two tables fall to the next host on their own: a vendor that lists a
// model with limits but no price must not leave the row unpriced.
func TestBuildTableFillsTheTablesApart(t *testing.T) {
	d := doc{
		"openai": {Models: map[string]row{
			"m": {Limit: &limit{Context: 200000}},
		}},
		"zzz-relay": {Models: map[string]row{
			"m": {Cost: &cost{Input: 3}},
		}},
	}
	c := buildTable(d)
	if got := c.Models["m"]; got != (Facts{Context: 200000}) {
		t.Errorf("limits = %+v", got)
	}
	if got := c.Prices["m"].Input; got != 3 {
		t.Errorf("price = %v", got)
	}
}

// A reply limit as long as the window is no reply limit: it is left out, so the
// engine keeps its own default rather than being told to write a whole window.
func TestFactsOfDropsAReplyAsLongAsTheWindow(t *testing.T) {
	if got := factsOf(&limit{Context: 262144, Output: 262144}); got.MaxOutput != 0 {
		t.Errorf("a reply as long as the window was kept: %+v", got)
	}
	// The provider's own window and the catalog's reply are compared too.
	p := config.Provider{ContextWindow: 100000, MaxTokens: 32000}
	cacheFrom(t, catalog{Models: map[string]Facts{"m": {Context: 1000000, MaxOutput: 128000}}})
	if got := For(config.Provider{ContextWindow: 100000}, "m"); got.MaxOutput != 0 {
		t.Errorf("a reply longer than the provider's window was kept: %+v", got)
	}
	if got := For(p, "m"); got != (Facts{Context: 100000, MaxOutput: 32000}) {
		t.Errorf("the provider's own numbers = %+v", got)
	}
}

// A relay's error answer must not replace the catalog: an empty table would
// leave every engine its own default until the next sync.
func TestReadDocRefusesANonAnswer(t *testing.T) {
	answer := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
	}
	good := `{"openai":{"models":{"m":{"limit":{"context":200000}}}}} `

	if _, err := readDoc(answer(http.StatusTooManyRequests, `{"error":{"code":429}}`)); err == nil {
		t.Error("a 429 with a JSON body was taken for a catalog")
	}
	if _, err := readDoc(answer(http.StatusOK, `{}`)); err == nil {
		t.Error("a catalog of no model was accepted")
	}
	if _, err := readDoc(answer(http.StatusOK, `{"openai":{"models":{}}}`)); err == nil {
		t.Error("a catalog of no model was accepted")
	}
	if _, err := readDoc(answer(http.StatusOK, good)); err != nil {
		t.Errorf("a good catalog was refused: %v", err)
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
	cacheFrom(t, catalog{Models: map[string]Facts{
		"m":      {Context: 200000, MaxOutput: 64000},
		"vendor": {Context: 1000000},
	}})
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
	path := filepath.Join(dir, cacheName)
	if err := writeTable(path, catalog{Models: map[string]Facts{"a": {Context: 1000000}}}); err != nil {
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
	path := filepath.Join(dir, cacheName)
	if err := writeTable(path, catalog{Models: map[string]Facts{"a": {Context: 1000000}}}); err != nil {
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
		t.Errorf("a cache two days old was taken for fresh: %+v", t2)
	}
	// However old, it still answers: an old window beats none.
	if got := readTable(path); len(got.Models) != 1 {
		t.Errorf("a stale cache was not readable: %+v", got)
	}
}
