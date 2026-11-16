package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// writeCatalog puts a catalog where ak keeps it, in the shape modelmeta reads,
// and points the package at it. models is the JSON of the catalog's models map.
func writeCatalog(t *testing.T, models string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	data := `{"models":` + models + `}`
	if err := os.WriteFile(filepath.Join(dir, "model-meta.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
