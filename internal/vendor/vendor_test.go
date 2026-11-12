package vendor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A user vendor is added from vendors.d, one with a built-in's id replaces
// it, and a broken file is reported and skipped.
func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("bili.toml", `id = "bili"
name = "bilibili AI Coding"
quota = "bili"
[engines.claude]
base_url = "http://bili.example/api"
[engines.codex]
base_url = "http://bili.example/api/v1/codex"
model = "gpt"
`)
	write("kimi.toml", "id = \"kimi\"\n[engines.pi]\nbase_url = \"https://mine\"\n")
	write("bad.toml", "id = \"bad\"\ntypo = 1\n")
	LoadDir(dir)
	t.Cleanup(func() { LoadDir("") })

	v, ok := Lookup("bili")
	if !ok || v.Quota != "bili" || len(v.Kinds()) != 2 || v.Engines["codex"].Model != "gpt" {
		t.Fatalf("bili = %+v, %v", v, ok)
	}
	if k, _ := Lookup("kimi"); len(k.Kinds()) != 1 || k.Engines["pi"].BaseURL != "https://mine" {
		t.Errorf("kimi was not replaced: %+v", k)
	}
	if errs := Errors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "bad.toml") {
		t.Errorf("errors = %v", errs)
	}
}
