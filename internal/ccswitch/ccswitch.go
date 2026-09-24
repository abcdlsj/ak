// Package ccswitch reads provider definitions from a cc-switch SQLite
// database.
//
// cc-switch owns ~/.cc-switch; ak only ever reads it, to offer an import. No
// function here writes, migrates or deletes cc-switch data, and the connection
// is opened read-only so that even a stray statement could not.
package ccswitch

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// The two engines ak can run. Anything else in the database is reported and
// skipped.
const (
	AppClaude = "claude"
	AppCodex  = "codex"
)

// Raw is one provider row exactly as cc-switch stores it. Parsing the
// settings_config and meta JSON is left to the caller.
type Raw struct {
	ID        string
	AppType   string
	Name      string
	Settings  string // settings_config, a JSON object
	Meta      string // meta, a JSON object
	IsCurrent bool
	// Endpoints are the base URLs cc-switch keeps in provider_endpoints for
	// endpoint auto-selection, in the order they were added. They are a
	// fallback for providers whose env block carries no base URL.
	Endpoints []string
}

// DBPath returns ~/.cc-switch/cc-switch.db.
func DBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cc-switch", "cc-switch.db"), nil
}

// Scan reads every provider from the default database.
func Scan() ([]Raw, error) {
	path, err := DBPath()
	if err != nil {
		return nil, err
	}
	return ScanFile(path)
}

// ScanFile reads every provider from path. The database is opened read-only,
// so a scan is safe to run while cc-switch is open.
func ScanFile(path string) ([]Raw, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s does not exist; cc-switch may never have been used", path)
		}
		return nil, err
	}

	db, err := sql.Open("sqlite", readOnlyURI(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()
	// One connection keeps the read consistent and avoids reopening the file
	// for every row.
	db.SetMaxOpenConns(1)

	const query = `SELECT id, app_type, name, settings_config,
	                      COALESCE(meta, ''), COALESCE(is_current, 0)
	               FROM providers
	               ORDER BY app_type, sort_index, name`
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("read providers from %s: %w", path, err)
	}
	defer rows.Close()

	var out []Raw
	for rows.Next() {
		var r Raw
		if err := rows.Scan(&r.ID, &r.AppType, &r.Name, &r.Settings, &r.Meta, &r.IsCurrent); err != nil {
			return nil, fmt.Errorf("read providers from %s: %w", path, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read providers from %s: %w", path, err)
	}

	// Endpoints live in a separate table and are optional: a database from a
	// cc-switch build without it still scans, just without the URL fallback.
	if endpoints, err := scanEndpoints(db); err == nil {
		for i := range out {
			out[i].Endpoints = endpoints[out[i].AppType+"\x00"+out[i].ID]
		}
	}
	return out, nil
}

// scanEndpoints reads the per-provider base URLs used by endpoint
// auto-selection, keyed by app type and provider id.
func scanEndpoints(db *sql.DB) (map[string][]string, error) {
	const query = `SELECT provider_id, app_type, url
	               FROM provider_endpoints
	               ORDER BY added_at, id`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var id, appType, url string
		if err := rows.Scan(&id, &appType, &url); err != nil {
			return nil, err
		}
		if url == "" {
			continue
		}
		key := appType + "\x00" + id
		out[key] = append(out[key], url)
	}
	return out, rows.Err()
}

// readOnlyURI builds a SQLite file URI that cannot write. The URL form escapes
// spaces and other reserved characters in the path.
func readOnlyURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String() + "?mode=ro"
}
