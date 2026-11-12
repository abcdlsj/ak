package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
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

func TestServeAdminPassword(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if _, err := runRoot(t, "short\n", "serve", "admin", "set-password"); err == nil {
		t.Error("a short password was taken")
	}
	if _, err := runRoot(t, "a-long-password\n", "serve", "admin", "set-password"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if !strings.HasPrefix(cfg.Service.AdminPassword, "$2a$") {
		t.Errorf("the password is not kept as bcrypt: %q", cfg.Service.AdminPassword)
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
