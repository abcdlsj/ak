package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/provider"
)

// antigravityProvider is a provider whose directory holds no credential yet.
func antigravityProvider(t *testing.T) (config.Provider, string) {
	t.Helper()
	dir := t.TempDir()
	return config.Provider{Kind: config.KindAntigravity, GeminiDir: dir}, dir
}

// importSource is the credential document agy writes, for the import paths.
func writeCredential(t *testing.T, dir string) {
	t.Helper()
	raw := `{"token":{"access_token":"ya29.a","token_type":"Bearer","refresh_token":"1//r","expiry":"2026-11-16T07:04:59Z"},"auth_method":"consumer"}`
	if err := provider.WriteAntigravityToken(dir, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

// A provider without a credential has to say so before the launch, not fail
// inside agy where the message is about signing in to Antigravity.
func TestAgyNotSignedInIsReported(t *testing.T) {
	p, _ := antigravityProvider(t)
	cfg := config.Default()
	cfg.Providers["work"] = p

	st := core.QuickStatus(cfg, "work")
	if !st.NeedsLogin || st.NoKey {
		t.Fatalf("status = %+v, want NeedsLogin only", st)
	}
	for _, problem := range st.Problems() {
		if problem == "not signed in" {
			return
		}
	}
	t.Errorf("problems = %q", st.Problems())
}

// Once the directory holds a credential the provider is ready: agy has no key,
// and ak must not ask for one.
func TestAgySignedInNeedsNoKey(t *testing.T) {
	p, dir := antigravityProvider(t)
	writeCredential(t, dir)
	cfg := config.Default()
	cfg.Providers["work"] = p

	st := core.QuickStatus(cfg, "work")
	if st.NeedsLogin || st.NoKey {
		t.Fatalf("status = %+v", st)
	}
}

// The listing names the account, and the directory when it cannot.
func TestAgyListColumns(t *testing.T) {
	p, dir := antigravityProvider(t)
	writeCredential(t, dir)

	if got := agyAccount(p); got != "" {
		t.Errorf("account = %q, want none without an ID token", got)
	}
	if got := agyState(p); got != "ok" {
		t.Errorf("state = %q, want ok (a refresh token renews it)", got)
	}

	rows := []listRow{{Name: "work", GeminiDir: p.GeminiDir, AgyState: "ok"}}
	if got := runsOn(rows[0]); got == "" || got == "agy "+"not signed in" {
		t.Errorf("runs on = %q", got)
	}

	// A provider with no credential says so rather than naming a directory
	// the user never sees.
	rows[0].AgyState = "missing"
	if got := runsOn(rows[0]); got == "" {
		t.Error("runs on is empty for a provider that is not signed in")
	}
}

// An import takes the antigravity-cli directory of another gemini_dir, or the
// document itself; both are what a user has at hand.
func TestAgyImportSources(t *testing.T) {
	source, _ := antigravityProvider(t)
	writeCredential(t, source.GeminiDir)

	fromDir, err := readAntigravityCredential(source.GeminiDir)
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := readAntigravityCredential(provider.AntigravityTokenPath(source.GeminiDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(fromDir) != string(fromFile) || len(fromDir) == 0 {
		t.Fatalf("directory and file sources differ (%d vs %d bytes)", len(fromDir), len(fromFile))
	}

	// A missing source is an error, not an empty credential.
	if _, err := readAntigravityCredential(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing source was accepted")
	}
}

// The token view never prints the credential itself, only what it means.
func TestAgyTokenRowHasNoSecret(t *testing.T) {
	p, dir := antigravityProvider(t)
	writeCredential(t, dir)
	cfg := config.Default()
	cfg.Providers["work"] = p

	row, err := antigravityTokenRow(cfg, "work")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "ok" || row.Path != provider.AntigravityTokenPath(dir) {
		t.Errorf("row = %+v", row)
	}
	if row.Account != "" {
		t.Errorf("account = %q", row.Account)
	}
}

// An antigravity provider is not a pool member, and the doctor has to notice
// two providers sharing one directory: they would share one account.
func TestAgySharedDirectoryIsReported(t *testing.T) {
	p, dir := antigravityProvider(t)
	writeCredential(t, dir)
	cfg := config.Default()
	cfg.Providers["one"] = p
	cfg.Providers["two"] = p

	d := &doctor{}
	doctorAntigravity(d, cfg)
	if d.problems == 0 {
		t.Error("two providers on one directory passed the check")
	}
}

// A claude provider must not read a gemini_dir, and an agy provider must not
// launch without one.
func TestAgyConfigSurface(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["work"] = config.Provider{Kind: config.KindAntigravity, GeminiDir: "/tmp/a"}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("ok provider rejected: %v", err)
	}

	// agy ignores a base_url and a key, so both are reported rather than kept.
	for name, p := range map[string]config.Provider{
		"base_url": {Kind: config.KindAntigravity, GeminiDir: "/tmp/a", BaseURL: "https://x"},
		"key":      {Kind: config.KindAntigravity, GeminiDir: "/tmp/a", APIKey: "k"},
		"no dir":   {Kind: config.KindAntigravity},
	} {
		cfg := config.Default()
		cfg.Providers[name] = p
		if err := config.Validate(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The hook a warren host installs goes in the provider's own config directory,
// which is not the machine's ~/.gemini/config.
func TestAgyPathsForHooks(t *testing.T) {
	p, dir := antigravityProvider(t)
	cfg := config.Default()
	cfg.Providers["work"] = p

	if got := provider.AntigravityConfigPath(p.GeminiDir); got != filepath.Join(dir, "config") {
		t.Errorf("config dir = %q", got)
	}
	if _, err := os.Stat(provider.AntigravityTokenPath(p.GeminiDir)); !os.IsNotExist(err) {
		t.Error("a provider without a credential has a token file")
	}
}

// The command that adds the credential must not be the one the missing
// credential refuses: `ak agy login` on a fresh provider is a launch, and a
// launch is what writes the token.
func TestAgySignInLaunchIsNotRefused(t *testing.T) {
	p, _ := antigravityProvider(t)
	st := core.Status{NeedsLogin: true}

	if err := launchRefusal("work", p, st, false); err == nil {
		t.Fatal("a launch with no credential was allowed")
	}
	if err := launchRefusal("work", p, st, true); err != nil {
		t.Errorf("signing in was refused: %v", err)
	}

	// A keyed engine has nothing to sign in, so the exemption does not cover it.
	claude := config.Provider{Kind: config.KindClaude, BaseURL: "https://a"}
	if err := launchRefusal("work", claude, core.Status{NoKey: true}, true); err == nil {
		t.Error("a keyed engine was launched to sign in")
	}
}

// A directory in the shared agy arguments replaces every provider's own, since
// ak's --gemini_dir comes first and the later flag wins. Doctor names it.
func TestAgyDirFlagInSharedArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--dangerously-skip-permissions"}, ""},
		{[]string{"--gemini_dir", "/tmp/g"}, "--gemini_dir"},
		{[]string{"--app_data_dir=/tmp/a"}, "--app_data_dir"},
		{[]string{"--gemini_dir"}, ""},
	}
	for _, c := range cases {
		if got := antigravityDirFlag(c.args); got != c.want {
			t.Errorf("antigravityDirFlag(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestNativeProviderListAndAddFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cxDir := filepath.Join(home, ".local/share/ak/codex/plus")
	if err := os.MkdirAll(cxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cxDir, "auth.json"), []byte(`{"tokens":{"access_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clDir := filepath.Join(home, ".local/share/ak/claude/max")
	piDir := filepath.Join(home, ".local/share/ak/pi/sub")

	cfg := config.Default()
	cfg.Providers["plus"] = config.Provider{Kind: config.KindCodex, CodexHome: "~/.local/share/ak/codex/plus"}
	cfg.Providers["max"] = config.Provider{Kind: config.KindClaude, ConfigDir: "~/.local/share/ak/claude/max"}
	cfg.Providers["sub"] = config.Provider{Kind: config.KindPi, PiDir: "~/.local/share/ak/pi/sub"}

	rows := listRows(cfg, core.RemoteModels{})
	byName := map[string]listRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	if r := byName["plus"]; r.NativeState != "ok" || runsOn(r) != "codex "+shortPath(cxDir) {
		t.Errorf("codex row = %+v, runsOn = %q", r, runsOn(r))
	}
	if r := byName["max"]; r.NativeState != "unknown" || runsOn(r) != "claude "+shortPath(clDir) {
		t.Errorf("claude row = %+v, runsOn = %q", r, runsOn(r))
	}
	if r := byName["sub"]; r.NativeState != "missing" || !strings.Contains(runsOn(r), "not signed in") || shortPath(piDir) == "" {
		t.Errorf("pi row = %+v, runsOn = %q", r, runsOn(r))
	}

	if k, ok := parseKind("agy"); !ok || k != config.KindAntigravity {
		t.Errorf("parseKind(agy) = %q, %v", k, ok)
	}
	if got := config.DefaultHomeDir(config.KindCodex, "plus"); got != cxDir {
		t.Errorf("DefaultHomeDir(codex, plus) = %q, want %q", got, cxDir)
	}

	cmd := newAddCmd()
	if err := cmd.Flags().Set("kind", "agy"); err != nil {
		t.Fatal(err)
	}
	var p config.Provider
	applyProviderFlags(cmd, &p, false)
	if p.Kind != config.KindAntigravity {
		t.Errorf("applyProviderFlags(--kind agy) -> Kind = %q, want %q", p.Kind, config.KindAntigravity)
	}
}
