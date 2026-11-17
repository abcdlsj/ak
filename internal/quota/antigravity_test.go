package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// agyPlan is what `agy -p "/usage" --output-format json` printed, cut down to
// the fields ak reads.
const agyPlan = `{"conversation_id":"","status":"SUCCESS","duration_seconds":0,"num_turns":0,
"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0},
"command":{"name":"usage","data":{"description":"Within each group, models share a weekly limit and a 5-hour limit.",
"groups":[
 {"name":"Gemini Models","description":"Models within this group: Gemini 4 Carbon","buckets":[
  {"id":"gemini-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0.9329,"reset_time":"2026-11-24T05:27:12Z"},
  {"id":"gemini-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":0.764,"reset_time":"2026-11-17T10:27:12Z"}]},
 {"name":"Claude and GPT models","description":"Models within this group: Claude Opus 5.5","buckets":[
  {"id":"3p-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":1,"reset_time":"2026-11-24T07:06:39Z"},
  {"id":"3p-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"2026-11-17T12:06:39Z"}]}]}}}`

// The reply becomes a plan: one window per group and allowance, with the share
// used and the reset in local time.
func TestParseAgyUsage(t *testing.T) {
	q, err := parseAgyUsage([]byte(agyPlan))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.Kind != "plan" || len(q.Windows) != 4 {
		t.Fatalf("quota = %+v", q)
	}
	if q.Windows[0].Name != "Gemini weekly" || q.Windows[0].Used != 6.7 {
		t.Errorf("first window = %+v, want Gemini weekly at 6.7%% used", q.Windows[0])
	}
	if q.Windows[1].Name != "Gemini 5h" || q.Windows[1].Used != 23.6 {
		t.Errorf("second window = %+v", q.Windows[1])
	}
	if q.Windows[2].Name != "Claude and GPT weekly" || q.Windows[2].Used != 0 {
		t.Errorf("third window = %+v", q.Windows[2])
	}
	if q.Windows[0].Resets == nil || !q.Windows[0].Resets.Equal(time.Date(2026, 11, 24, 5, 27, 12, 0, time.UTC)) {
		t.Errorf("reset = %v", q.Windows[0].Resets)
	}
}

// A command agy does not know is not an error to agy: it spends a turn and
// prints that conversation instead, so ak must not read it as a plan.
func TestParseAgyUsageRefusesAnotherCommand(t *testing.T) {
	other := strings.Replace(agyPlan, `"name":"usage"`, `"name":""`, 1)
	if _, err := parseAgyUsage([]byte(other)); err == nil || !strings.Contains(err.Error(), "instead of /usage") {
		t.Errorf("a turn was read as a plan: %v", err)
	}
	failed := strings.Replace(agyPlan, `"status":"SUCCESS"`, `"status":"ERROR","error":"quota unavailable"`, 1)
	if _, err := parseAgyUsage([]byte(failed)); err == nil || !strings.Contains(err.Error(), "quota unavailable") {
		t.Errorf("a failure was read as a plan: %v", err)
	}
	if _, err := parseAgyUsage([]byte(`{"command":{"name":"usage"}}`)); err == nil {
		t.Error("an empty plan was accepted")
	}
	if _, err := parseAgyUsage([]byte("Fetching…\nnot json")); err == nil {
		t.Error("junk was accepted")
	}
}

// fakeAgy writes a stand-in agy that answers the plan and records the arguments
// and the environment it was given.
func fakeAgy(t *testing.T) (bin, argsFile, envFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "agy")
	argsFile = filepath.Join(dir, "args")
	envFile = filepath.Join(dir, "env")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argsFile + "\n" +
		"env > " + envFile + "\n" +
		"cat <<'JSON'\n" + agyPlan + "\nJSON\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile, envFile
}

// signedInDir is a provider directory holding a credential agy could read.
func signedInDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw := `{"token":{"access_token":"ya29.a","refresh_token":"1//r","expiry":"2026-11-16T07:04:59Z"}}`
	if err := provider.WriteAntigravityToken(dir, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ak asks agy, and only agy: the engine's own environment, its own directory,
// its own print-mode usage command.
func TestAntigravityQuotaAsksAgy(t *testing.T) {
	bin, argsFile, envFile := fakeAgy(t)
	t.Setenv("AK_AGY_BIN", bin)
	dir := signedInDir(t)

	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: dir, Quota: "antigravity"}
	q := Query(context.Background(), "work", p, "")
	if q.Error != "" {
		t.Fatalf("query: %s", q.Error)
	}
	if q.Source != "antigravity" || len(q.Windows) != 4 {
		t.Fatalf("quota = %+v", q)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "--gemini_dir\n" + dir + "\n-p\n/usage\n--output-format\njson\n"
	if string(args) != want {
		t.Errorf("agy was run with\n%s\nwant\n%s", args, want)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	// The credential file is the one agy reads only when it looks remote.
	if !strings.Contains(string(env), "SSH_CONNECTION=") {
		t.Error("agy was not told to keep its credential in the provider's directory")
	}
}

// Asking agy costs agy's own startup, so the source is named rather than
// detected: a provider without quota = antigravity is left alone, `off` is
// still off, and a provider of another kind cannot ask agy for anything.
func TestAntigravityResolve(t *testing.T) {
	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/x"}
	if _, err := resolve(p); !errors.Is(err, ErrNoSource) {
		t.Errorf("an unnamed source resolved: %v", err)
	}
	p.Quota = "antigravity"
	if src, err := resolve(p); err != nil || src.ID() != "antigravity" {
		t.Errorf("resolve = %v, %v", src, err)
	}
	p.Quota = "off"
	if _, err := resolve(p); err == nil {
		t.Error("quota = off still resolved a source")
	}
	p = config.Provider{Kind: config.KindClaude, BaseURL: "https://api.anthropic.com", Quota: "antigravity"}
	if q := Query(context.Background(), "c", p, ""); q.Error == "" || !strings.Contains(q.Error, "kind") {
		t.Errorf("a claude provider asked agy: %+v", q)
	}
}

// Nothing to ask when nobody signed in: agy would answer its sign-in screen.
func TestAntigravityQuotaNeedsASignIn(t *testing.T) {
	t.Setenv("AK_AGY_BIN", "/nonexistent/agy")
	p := config.Provider{Kind: config.KindAntigravity, GeminiDir: t.TempDir(), Quota: "antigravity"}
	q := Query(context.Background(), "work", p, "")
	if !strings.Contains(q.Error, "not signed in") {
		t.Errorf("error = %q, want a sign-in hint", q.Error)
	}
}
