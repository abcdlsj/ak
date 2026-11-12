package quota

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// An executable in quota.d is a source by its file name: it gets the key
// and the account's secrets in the environment, and prints the quota.
func TestProgramSource(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
[ "$AK_QUOTA_KEY" = k ] || { echo "wrong key" >&2; exit 1; }
echo "{\"windows\":[{\"name\":\"daily\",\"used\":42}],\"detail\":\"$AK_QUOTA_VAR_CONTROL\"}"
`
	if err := os.WriteFile(filepath.Join(dir, "bili.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := loadRegistry(dir)
	if len(r.errs) > 0 {
		t.Fatal(r.errs)
	}
	var src Source
	for _, s := range r.sources {
		if s.ID() == "bili" {
			src = s
		}
		if s.ID() == "notes" {
			t.Error("a file that is not executable became a source")
		}
	}
	if src == nil {
		t.Fatal("bili.sh is not a source")
	}
	p := config.Provider{Secrets: map[string]string{"control": "c1"}}
	q, err := src.Fetch(context.Background(), p, "k")
	if err != nil || len(q.Windows) != 1 || q.Windows[0].Used != 42 || q.Detail != "c1" {
		t.Fatalf("q = %+v, err %v", q, err)
	}
	if _, err := src.Fetch(context.Background(), p, "bad"); err == nil || !strings.Contains(err.Error(), "wrong key") {
		t.Fatalf("a failing program said %v", err)
	}
}
