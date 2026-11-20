package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/gateway"
)

// runRoot runs ak with args, stdin and stdout swapped for the test.
func runRoot(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	in, _ := os.CreateTemp(t.TempDir(), "in")
	in.WriteString(stdin)
	in.Seek(0, 0)
	r, w, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, w
	root := newRoot()
	root.SetArgs(args)
	err := root.Execute()
	w.Close()
	os.Stdin, os.Stdout = oldIn, oldOut
	var b bytes.Buffer
	io.Copy(&b, r)
	return b.String(), err
}

// runRootErr runs ak with stdin swapped and captures stdout and stderr.
func runRootErr(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	in, _ := os.CreateTemp(t.TempDir(), "in")
	in.WriteString(stdin)
	in.Seek(0, 0)
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, wOut, wErr
	root := newRoot()
	root.SetArgs(args)
	err := root.Execute()
	wOut.Close()
	wErr.Close()
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	var bo, be bytes.Buffer
	io.Copy(&bo, rOut)
	io.Copy(&be, rErr)
	return bo.String(), be.String(), err
}

func TestServeTokens(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	got, err := runRoot(t, "", "serve", "token", "add", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(got)
	cfg, _ := config.Load()
	if !strings.HasPrefix(tok, "ak_") || cfg.Service.Tokens["laptop"].Hash != gateway.HashToken(tok) {
		t.Fatalf("token %q not kept as its hash: %+v", tok, cfg.Service.Tokens)
	}
	b, _ := os.ReadFile(os.Getenv(config.DirEnv) + "/providers.toml")
	if strings.Contains(string(b), tok) {
		t.Fatal("the token itself was written")
	}
	if _, err := runRoot(t, "", "serve", "token", "add", "laptop"); err == nil {
		t.Error("a token was issued twice under one name")
	}
	if _, err := runRoot(t, "", "serve", "token", "add", "phone", "--route", "nope"); err == nil {
		t.Error("a token was limited to nothing that exists")
	}
	if list, _ := runRoot(t, "", "serve", "token", "list"); !strings.Contains(list, "laptop") || strings.Contains(list, tok) {
		t.Errorf("list = %q", list)
	}
	if _, err := runRoot(t, "", "serve", "token", "revoke", "laptop"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); len(cfg.Service.Tokens) != 0 {
		t.Error("revoke left the token")
	}
}

// A service that knows its public URL prints the line that sets a machine up.
func TestServeTokenPrintsRemoteAdd(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ChangeService(cfg, func(s *config.Service) { s.PublicURL = "https://ak.example.com/" }); err != nil {
		t.Fatal(err)
	}
	tok, said, err := runRootErr(t, "", "serve", "token", "add", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	tok = strings.TrimSpace(tok)
	if want := "ak remote add laptop https://ak.example.com --token " + tok; !strings.Contains(said, want) {
		t.Fatalf("stderr = %q, want %q in it", said, want)
	}
	// With no public URL it says how to set one.
	if err := core.ChangeService(cfg, func(s *config.Service) { s.PublicURL = "" }); err != nil {
		t.Fatal(err)
	}
	if _, said, err = runRootErr(t, "", "serve", "token", "add", "phone"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "service.public_url") {
		t.Fatalf("stderr = %q, want the public_url hint", said)
	}
}

func TestServeRefusesPublicWithoutTokens(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	_, err := runRoot(t, "", "serve", "--addr", "127.0.0.1:0", "--service", "0.0.0.0:0")
	if err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("serve on a public address with no token: %v", err)
	}
}

func TestServeRoutesAndTokenJSON(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())

	cfg := config.Default()
	cfg.Accounts["goat"] = config.Account{Vendor: "command-code", APIKey: "k1"}
	cfg.Accounts["od"] = config.Account{Vendor: "opendesign", APIKey: "k2"}
	cfg.Resolve()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := runRoot(t, "", "serve", "route", "add", "fast", "--model", "goat/deepseek-v4"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(t, "", "serve", "route", "add", "main", "--member", "goat", "--member", "od", "--strategy", "smart"); err != nil {
		t.Fatal(err)
	}
	listOut, err := runRoot(t, "", "serve", "route", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fast", "main", "smart", "goat/deepseek-v4"} {
		if !strings.Contains(listOut, want) {
			t.Errorf("route list lacks %q:\n%s", want, listOut)
		}
	}
	jsonOut, err := runRoot(t, "", "serve", "route", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut, `"main"`) || !strings.Contains(jsonOut, `"smart"`) {
		t.Errorf("route list json:\n%s", jsonOut)
	}
	if _, err := runRoot(t, "", "serve", "route", "rm", "fast"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()
	if _, ok := cfg.Service.Routes["fast"]; ok {
		t.Error("rm left route fast")
	}

	if _, err := runRoot(t, "", "serve", "token", "add", "laptop", "--route", "main"); err != nil {
		t.Fatal(err)
	}
	tokJSON, err := runRoot(t, "", "serve", "token", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tokJSON, `"laptop"`) || !strings.Contains(tokJSON, `"main"`) {
		t.Errorf("token list json:\n%s", tokJSON)
	}
}
