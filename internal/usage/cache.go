package usage

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// Cache holds incremental scan state: per file, its pre-aggregated buckets and
// how far it has been parsed. Repeat scans only read new bytes.
type Cache struct {
	// Version invalidates the whole cache when the format or parsing changes.
	Version int
	// Zone invalidates the cache when the local time zone changes, since
	// buckets are keyed by local date.
	Zone  string
	Files map[string]*fileState
}

const cacheVersion = 2

func newCache() *Cache {
	return &Cache{Version: cacheVersion, Zone: zoneID(), Files: map[string]*fileState{}}
}

func zoneID() string {
	name, off := time.Now().Zone()
	return fmt.Sprintf("%s%+d", name, off)
}

func cachePath() (string, error) {
	dir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "usage-cache.gob"), nil
}

// loadCache returns the cache, or an empty one when it is missing, stale or
// unreadable; a cache problem only costs a rescan.
func loadCache() *Cache {
	p, err := cachePath()
	if err != nil {
		return newCache()
	}
	// The v1 cache stored every request as JSON and grew past 100 MB.
	_ = os.Remove(filepath.Join(filepath.Dir(p), "usage-cache.json"))

	f, err := os.Open(p)
	if err != nil {
		return newCache()
	}
	defer f.Close()
	var c Cache
	if gob.NewDecoder(f).Decode(&c) != nil ||
		c.Version != cacheVersion || c.Zone != zoneID() || c.Files == nil {
		return newCache()
	}
	return &c
}

func saveCache(c *Cache) error {
	p, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".usage-cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := gob.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
