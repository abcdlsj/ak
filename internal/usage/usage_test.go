package usage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

func claudeLine(id, session, ts string, in, out, cacheRead int) string {
	return `{"type":"assistant","timestamp":"` + ts + `","sessionId":"` + session +
		`","message":{"id":"` + id + `","model":"claude-x","usage":{"input_tokens":` + itoa(in) +
		`,"output_tokens":` + itoa(out) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `}}}` + "\n"
}

func codexTokenLine(ts string, total, in, cached, out int) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{` +
		`"total_token_usage":{"total_tokens":` + itoa(total) + `},` +
		`"last_token_usage":{"input_tokens":` + itoa(in) + `,"cached_input_tokens":` + itoa(cached) +
		`,"output_tokens":` + itoa(out) + `}}}}` + "\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	// Force a visible mtime change even on coarse-grained filesystems.
	later := time.Now().Add(time.Second)
	_ = os.Chtimes(path, later, later)
}

type fixture struct {
	claude, codex string
	cache         *Cache
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	return &fixture{
		claude: filepath.Join(dir, "claude"),
		codex:  filepath.Join(dir, "codex"),
		cache:  newCache(),
	}
}

func (f *fixture) scan() []bucket {
	roots := map[Source][]string{
		claudeSource{}: {f.claude},
		codexSource{}:  {f.codex},
	}
	return scan(listFiles(roots), f.cache)
}

func sum(bs []bucket) (Tokens, int) {
	var t Tokens
	n := 0
	for _, b := range bs {
		t.add(b.Tokens)
		n += b.Requests
	}
	return t, n
}

func withZone(t *testing.T, loc *time.Location) {
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
}

// A message is written once per content block; only the last line's usage counts.
func TestClaudeStreamedMessageCountsOnce(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.claude, "p", "s1.jsonl"),
		claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 100, 0, 50)+
			claudeLine("m1", "s1", "2026-09-01T10:00:01Z", 100, 30, 50)+
			claudeLine("m2", "s1", "2026-09-01T10:01:00Z", 10, 5, 0))

	tok, req := sum(f.scan())
	if req != 2 {
		t.Fatalf("requests = %d, want 2", req)
	}
	if tok.Input != 110 || tok.Output != 35 || tok.CacheRead != 50 {
		t.Fatalf("tokens = %+v", tok)
	}
}

// A resumed or forked session copies history into a new file.
func TestClaudeCopiedHistoryCountsOnce(t *testing.T) {
	f := newFixture(t)
	orig := filepath.Join(f.claude, "p", "a.jsonl")
	writeFile(t, orig, claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 100, 10, 0))
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(orig, old, old)
	writeFile(t, filepath.Join(f.claude, "p", "b.jsonl"),
		claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 100, 10, 0)+
			claudeLine("m9", "s2", "2026-09-01T11:00:00Z", 1, 1, 0))

	tok, req := sum(f.scan())
	if req != 2 || tok.Input != 101 {
		t.Fatalf("requests = %d, tokens = %+v", req, tok)
	}
	// A rescan with everything cached must agree.
	tok2, req2 := sum(f.scan())
	if tok2 != tok || req2 != req {
		t.Fatalf("rescan = %+v/%d, want %+v/%d", tok2, req2, tok, req)
	}
}

// input_tokens includes cached_input_tokens, and a repeated event with an
// unchanged cumulative total is not a new turn.
func TestCodexCachedSplitAndRepeats(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.codex, "r.jsonl"),
		`{"timestamp":"2026-09-01T10:00:00Z","type":"session_meta","payload":{"model_provider":"relay"}}`+"\n"+
			`{"timestamp":"2026-09-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-a"}}`+"\n"+
			codexTokenLine("2026-09-01T10:00:01Z", 6512, 6272, 6144, 240)+
			codexTokenLine("2026-09-01T10:00:02Z", 6512, 6272, 6144, 240)+
			`{"timestamp":"2026-09-01T10:01:00Z","type":"turn_context","payload":{"model":"gpt-b"}}`+"\n"+
			codexTokenLine("2026-09-01T10:01:01Z", 6600, 80, 0, 8))

	bs := f.scan()
	tok, req := sum(bs)
	if req != 2 {
		t.Fatalf("requests = %d, want 2", req)
	}
	if tok.Input != 128+80 || tok.CacheRead != 6144 || tok.Output != 248 {
		t.Fatalf("tokens = %+v", tok)
	}
	models := map[string]bool{}
	for _, b := range bs {
		models[b.Model] = true
		if b.RawProvider != "relay" {
			t.Fatalf("raw provider = %q", b.RawProvider)
		}
	}
	if !models["gpt-a"] || !models["gpt-b"] {
		t.Fatalf("models = %v", models)
	}
}

// Appending must give the same result as a full parse, carrying codex state
// across the boundary and leaving an unterminated line for later.
func TestIncrementalMatchesFullParse(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.codex, "r.jsonl")
	head := `{"timestamp":"2026-09-01T10:00:00Z","type":"session_meta","payload":{"model_provider":"relay"}}` + "\n" +
		`{"timestamp":"2026-09-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-a"}}` + "\n" +
		codexTokenLine("2026-09-01T10:00:01Z", 100, 90, 0, 10)
	partial := codexTokenLine("2026-09-01T10:00:05Z", 200, 90, 0, 10)
	writeFile(t, path, head+partial[:20])

	if _, req := sum(f.scan()); req != 1 {
		t.Fatalf("first scan requests = %d, want 1", req)
	}
	appendFile(t, path, partial[20:])
	bs := f.scan()
	tok, req := sum(bs)
	if req != 2 || tok.Input != 180 {
		t.Fatalf("after append: requests = %d, tokens = %+v", req, tok)
	}
	for _, b := range bs {
		if b.Model != "gpt-a" || b.RawProvider != "relay" {
			t.Fatalf("lost context across increments: %+v", b)
		}
	}

	full := newFixture(t)
	full.codex = f.codex
	ftok, freq := sum(full.scan())
	if ftok != tok || freq != req {
		t.Fatalf("full parse = %+v/%d, incremental = %+v/%d", ftok, freq, tok, req)
	}
}

// A claude message split across two scans still counts once, with the final usage.
func TestIncrementalClaudeSupersede(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.claude, "p", "s.jsonl")
	writeFile(t, path, claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 100, 0, 0))
	f.scan()
	appendFile(t, path, claudeLine("m1", "s1", "2026-09-01T10:00:01Z", 100, 30, 0))
	tok, req := sum(f.scan())
	if req != 1 || tok.Output != 30 || tok.Input != 100 {
		t.Fatalf("requests = %d, tokens = %+v", req, tok)
	}
}

// The timeline sums each 5-minute slot, a superseded line counting once.
func TestTimelineSlots(t *testing.T) {
	withZone(t, time.UTC)
	f := newFixture(t)
	writeFile(t, filepath.Join(f.claude, "p", "s.jsonl"),
		claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 100, 0, 0)+
			claudeLine("m1", "s1", "2026-09-01T10:00:01Z", 100, 30, 0)+
			claudeLine("m2", "s1", "2026-09-01T10:07:00Z", 10, 5, 0))
	rows := attribute(f.scan(), &attribution{sessions: sessionIndex{}})
	got := Timeline(rows, Filter{})
	want := []Point{
		{time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), 130},
		{time.Date(2026, 9, 1, 10, 5, 0, 0, time.UTC), 15},
	}
	if len(got) != len(want) {
		t.Fatalf("timeline = %+v, want %+v", got, want)
	}
	for i := range want {
		if !got[i].Time.Equal(want[i].Time) || got[i].Tokens != want[i].Tokens {
			t.Fatalf("timeline = %+v, want %+v", got, want)
		}
	}
}

// Days split at local midnight, not UTC midnight.
func TestDatesAreLocal(t *testing.T) {
	withZone(t, time.FixedZone("CST", 8*3600))
	f := newFixture(t)
	f.cache = newCache()
	writeFile(t, filepath.Join(f.claude, "p", "s.jsonl"),
		claudeLine("m1", "s1", "2026-09-01T17:00:00Z", 1, 1, 0))
	bs := f.scan()
	if len(bs) != 1 || bs[0].Date != "2026-09-02" {
		t.Fatalf("buckets = %+v, want date 2026-09-02", bs)
	}
}

func TestSessionOwnerFollowsResume(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	idx := sessionIndex{"s": {
		{SessionID: "s", Provider: "a", TS: t0},
		{SessionID: "s", Provider: "b", TS: t0.Add(time.Hour)},
	}}
	cases := map[time.Duration]string{-time.Minute: "a", 30 * time.Minute: "a", 2 * time.Hour: "b"}
	for d, want := range cases {
		if got, _ := idx.owner("s", t0.Add(d)); got != want {
			t.Errorf("owner at %v = %q, want %q", d, got, want)
		}
	}
}

func TestAttributeCodexProviderID(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["mine"] = config.Provider{Kind: config.KindCodex, ProviderID: "relay"}
	rows := attribute([]bucket{
		{Date: "2026-09-01", Engine: "codex", Model: "m", RawProvider: "relay", Requests: 1},
		{Date: "2026-09-01", Engine: "codex", Model: "m", RawProvider: "other", Requests: 1},
		{Date: "2026-09-01", Engine: "claude", Model: "m", Session: "nope", Requests: 1},
	}, &attribution{sessions: sessionIndex{}, codexIDs: codexProviderNames(cfg)})
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Provider] = true
	}
	for _, want := range []string{"mine", "other", "claude"} {
		if !got[want] {
			t.Errorf("missing provider %q in %v", want, got)
		}
	}
}

func TestPriceBookPrefersFirstParty(t *testing.T) {
	one, two := 1.0, 2.0
	doc := map[string]modelsDevProvider{}
	add := func(prov, id string, in *float64) {
		p := doc[prov]
		if p.Models == nil {
			p.Models = map[string]struct {
				Cost *modelsDevCost `json:"cost"`
			}{}
		}
		p.Models[id] = struct {
			Cost *modelsDevCost `json:"cost"`
		}{&modelsDevCost{Input: in}}
		doc[prov] = p
	}
	add("aaa-relay", "gpt-a", &two)
	add("openai", "gpt-a", &one)
	add("zzz", "z-ai/glm", &two)

	b := buildPriceBook(doc)
	if p, _ := b.lookup("gpt-a"); p.Input != 1 {
		t.Fatalf("gpt-a input = %v, want the first-party price", p.Input)
	}
	b["glm-5"] = modelPrice{Input: 3}
	if p, ok := b.lookup("z-ai/GLM-5-preview"); !ok || p.Input != 3 {
		t.Fatalf("vendor-prefixed lookup = %+v, %v", p, ok)
	}
}

func TestSummarizeFilter(t *testing.T) {
	cfg := config.Default()
	prices = priceBook{}
	priceOnce.Do(func() {})
	rows := []Row{
		{Date: "2026-09-01", Provider: "a", Model: "m", Tokens: Tokens{Input: 1}, Requests: 1},
		{Date: "2026-09-10", Provider: "b", Model: "m", Tokens: Tokens{Input: 2}, Requests: 1},
	}
	if s := Summarize(rows, cfg, Filter{Since: "2026-09-05"}); s.TotalTokens != 2 {
		t.Fatalf("since filter total = %d", s.TotalTokens)
	}
	if s := Summarize(rows, cfg, Filter{Provider: "a"}); s.TotalTokens != 1 || s.UnpricedTokens != 1 {
		t.Fatalf("provider filter = %+v", s)
	}
}
