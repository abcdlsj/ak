package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnknownKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := Path()
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("version = 1\n[providers.a]\nkind = 'claude'\nbase_url = 'x'\nmodle = 'm'\n"), 0o600)
	keys, err := UnknownKeys()
	if err != nil || !reflect.DeepEqual(keys, []string{"providers.a.modle"}) {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
}
