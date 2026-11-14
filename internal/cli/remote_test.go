package cli

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/vendor"
)

// A service on one machine, registered on another as a remote.
func TestRemoteAddQuotaRm(t *testing.T) {
	// The server: its own config, an account on a local vendor whose quota
	// is off, so nothing is asked of a real one, and a token. The vendor
	// list is the process's, so the client's vendors.d holds it too.
	clientDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(clientDir+"/vendors.d", 0o700)
	os.WriteFile(clientDir+"/vendors.d/local.toml", []byte("id = \"local\"\nquota = \"off\"\n[engines.claude]\nbase_url = \"http://127.0.0.1:1\"\nmodel = \"m\"\n"), 0o600)
	vendor.LoadDir(clientDir + "/vendors.d")
	server := config.Default()
	server.Accounts["kimi"] = config.Account{Vendor: "local", APIKey: "upstream-key"}
	tok, hash, _ := gateway.NewToken()
	server.Service.Tokens = map[string]config.Token{"laptop": {Hash: hash}}
	server.Resolve()
	srv := gateway.New(server)
	srv.Logf = func(string, ...any) {}
	hs := httptest.NewServer(srv.ServiceHandler())
	defer hs.Close()

	// The client.
	t.Setenv(config.DirEnv, clientDir)
	t.Cleanup(func() { vendor.LoadDir(t.TempDir()) })
	if _, err := runRoot(t, "wrong\n", "remote", "add", "home", hs.URL, "--token", "-"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a wrong token was taken: %v", err)
	}
	if _, err := runRoot(t, tok+"\n", "remote", "add", "home", hs.URL, "--token", "-"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Accounts["home"].APIKey != tok {
		t.Fatal("the remote's account does not hold the token")
	}
	b, _ := os.ReadFile(clientDir + "/vendors.d/home.toml")
	if strings.Contains(string(b), tok) || strings.Contains(string(b), "upstream-key") {
		t.Fatal("a secret reached the vendor file")
	}

	// The usual flow: a provider on the remote's account.
	if err := core.Add(cfg, "work", config.Provider{Kind: config.KindCodex, Account: "home", Model: "kimi"}); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["work"]
	if p.BaseURL != hs.URL+"/v1" || p.APIKey != tok || p.WireAPI != "responses" {
		t.Errorf("codex on the remote = %+v", p)
	}

	// Its quota is the server's accounts', named under the remote.
	holders, _ := core.Holders(cfg, []string{"home"})
	q := queryQuotas(context.Background(), holders)
	if len(q) != 1 || q[0].Error != "" || len(q[0].Accounts) != 1 || q[0].Accounts[0].Provider != "home/kimi" ||
		!strings.Contains(q[0].Accounts[0].Error, "off") {
		t.Fatalf("quota = %+v", q)
	}
	if list, _ := runRoot(t, "", "remote", "list"); !strings.Contains(list, hs.URL) {
		t.Errorf("list = %q", list)
	}
	if _, err := runRoot(t, "", "remote", "rm", "home"); err == nil {
		t.Error("a remote a provider runs on was removed")
	}
	cfg, _ = config.Load()
	core.Remove(cfg, "work")
	if _, err := runRoot(t, "", "remote", "rm", "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clientDir + "/vendors.d/home.toml"); !os.IsNotExist(err) {
		t.Error("the vendor file was left")
	}
}

// --model sets a machine up in one command: a provider per engine on the
// remote's account, and no token again when it is re-run.
func TestRemoteAddProviders(t *testing.T) {
	clientDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(clientDir+"/vendors.d", 0o700)
	os.WriteFile(clientDir+"/vendors.d/local.toml", []byte("id = \"local\"\nquota = \"off\"\n[engines.claude]\nbase_url = \"http://127.0.0.1:1\"\nmodel = \"m\"\n"), 0o600)
	vendor.LoadDir(clientDir + "/vendors.d")
	server := config.Default()
	server.Accounts["kimi"] = config.Account{Vendor: "local", APIKey: "upstream-key"}
	server.Service.Routes = map[string]config.Route{"main": {Members: []string{"kimi"}}}
	tok, hash, _ := gateway.NewToken()
	server.Service.Tokens = map[string]config.Token{"laptop": {Hash: hash}}
	server.Resolve()
	srv := gateway.New(server)
	srv.Logf = func(string, ...any) {}
	hs := httptest.NewServer(srv.ServiceHandler())
	defer hs.Close()

	t.Setenv(config.DirEnv, clientDir)
	t.Cleanup(func() { vendor.LoadDir(t.TempDir()) })
	if _, err := runRoot(t, "", "remote", "add", "home", hs.URL, "--kind", "claude"); err == nil {
		t.Error("--kind without --model was taken")
	}
	said, err := runRoot(t, tok+"\n", "remote", "add", "home", hs.URL, "--token", "-", "--model", "main")
	if err != nil {
		t.Fatal(err)
	}
	// The model list is shown, so a route name can be picked from it.
	if !strings.Contains(said, "main") || !strings.Contains(said, "claude") {
		t.Errorf("the model list was not listed: %q", said)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"claude", "codex", "pi"} {
		p, ok := cfg.Providers["home-"+k]
		if !ok || p.Account != "home" || p.Kind != config.Kind(k) || p.Model != "main" {
			t.Fatalf("home-%s = %+v", k, p)
		}
	}

	// Re-run without the token: the kept key is used, and only --kind is changed.
	if _, err := runRoot(t, "", "remote", "add", "home", hs.URL, "--model", "main", "--kind", "claude"); err != nil {
		t.Fatal(err)
	}
	// A model the service does not list is taken, with a hint.
	if _, err := runRoot(t, "", "remote", "add", "home", hs.URL, "--model", "kimi/unlisted"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()
	if p := cfg.Providers["home-claude"]; p.Model != "kimi/unlisted" {
		t.Errorf("the model was not changed: %+v", p)
	}
	// The token never reached the vendor file.
	b, _ := os.ReadFile(clientDir + "/vendors.d/home.toml")
	if strings.Contains(string(b), tok) {
		t.Fatal("the token reached the vendor file")
	}
}
