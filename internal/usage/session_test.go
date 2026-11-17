package usage

import (
	"math"
	"path/filepath"
	"testing"
)

// hitOf is a session's hit rate, failing the test when it is unknown.
func hitOf(t *testing.T, s Session) float64 {
	t.Helper()
	h, ok := s.HitRate()
	if !ok {
		t.Fatalf("session %s has no hit rate, tokens %+v", s.ID, s.Tokens)
	}
	return h
}

func about(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Fatalf("hit rate = %.2f, want %.2f", got, want)
	}
}

// Every session gets its own row: cache reads over the whole prompt, and no
// figure at all when the session reported no cache activity.
func TestSessionsSplitBySession(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.claude, "p", "s1.jsonl"),
		claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 100, 10, 900)+
			claudeLine("m2", "s1", "2026-09-01T10:01:00Z", 100, 10, 900))
	writeFile(t, filepath.Join(f.claude, "p", "s2.jsonl"),
		claudeLine("m3", "s2", "2026-09-01T10:02:00Z", 50, 10, 0))

	got := sessions(f.scan(), &attribution{sessions: sessionIndex{}})
	if len(got) != 2 {
		t.Fatalf("sessions = %d, want 2: %+v", len(got), got)
	}
	// The larger session comes first; both ran on the engine's default login.
	if got[0].ID != "s1" || got[0].Provider != "claude" {
		t.Fatalf("first session = %+v", got[0])
	}
	want := Tokens{Input: 200, Output: 20, CacheRead: 1800}
	if got[0].Tokens != want || got[0].Requests != 2 {
		t.Fatalf("tokens = %+v/%d, want %+v/2", got[0].Tokens, got[0].Requests, want)
	}
	about(t, hitOf(t, got[0]), 90)
	if h, ok := got[1].HitRate(); ok {
		t.Fatalf("a session with no cache activity reported %.2f%%", h)
	}
}

// A session's last request is when it was last used, not its first.
func TestSessionsLastRequest(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.claude, "p", "s1.jsonl"),
		claudeLine("m1", "s1", "2026-09-01T10:00:00Z", 1, 1, 0)+
			claudeLine("m2", "s1", "2026-09-01T12:00:00Z", 1, 1, 0))
	got := sessions(f.scan(), &attribution{sessions: sessionIndex{}})
	if len(got) != 1 || got[0].Last.UTC().Hour() != 12 {
		t.Fatalf("sessions = %+v", got)
	}
}

// codex records the session id only in session_meta, and input_tokens already
// includes the cached part.
func TestSessionsCodexID(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.codex, "r.jsonl"),
		`{"timestamp":"2026-09-01T10:00:00Z","type":"session_meta","payload":{"session_id":"cx-1","model_provider":"relay"}}`+"\n"+
			codexTokenLine("2026-09-01T10:00:01Z", 6512, 6272, 6144, 240))

	got := sessions(f.scan(), &attribution{})
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	s := got[0]
	if s.Engine != "codex" || s.ID != "cx-1" || s.Provider != "relay" {
		t.Fatalf("session = %+v", s)
	}
	if s.Tokens.Input != 128 || s.Tokens.CacheRead != 6144 {
		t.Fatalf("tokens = %+v", s.Tokens)
	}
	about(t, hitOf(t, s), 97.96)
}

// pi names its session in the header line the file starts with.
func TestPiSessionHeader(t *testing.T) {
	p := piSource{}.NewParser(nil)
	if _, ok := p.Parse([]byte(`{"type":"session","version":3,"id":"pi-1","cwd":"/tmp"}`)); ok {
		t.Fatal("the session header produced a record")
	}
	rec, ok := p.Parse([]byte(piAssistant("a1", "2026-09-01T10:00:02.000Z", 1756720802000, "ak-relay", "glm-5", 100, 20, 50, 5, 8)))
	if !ok {
		t.Fatal("an assistant message was not parsed")
	}
	if rec.Session != "pi-1" {
		t.Fatalf("session = %q, want pi-1", rec.Session)
	}
	// A resumed parser keeps it across the state round trip.
	resumed := piSource{}.NewParser(p.State())
	rec2, ok := resumed.Parse([]byte(piAssistant("a2", "2026-09-01T10:00:03.000Z", 1756720803000, "ak-relay", "glm-5", 1, 1, 0, 0, 0)))
	if !ok || rec2.Session != "pi-1" {
		t.Fatalf("resumed record = %+v", rec2)
	}
}
