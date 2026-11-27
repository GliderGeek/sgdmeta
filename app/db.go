// Locating, opening and checking the metadata database.
//
// The database is produced by ../harvester (src/sgdmeta/index.py) and only
// ever read here. It records its schema version in PRAGMA user_version; the
// first Zenodo release predates that and has version 0, which is treated as
// version 1 when the expected tables are there.
package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure Go, includes FTS5: cross-compiles without cgo
)

const (
	dbFileName = "sgdmeta.sqlite3"

	minSchema = 1
	maxSchema = 1
)

// defaultDBPath is where a downloaded database goes, and where the program
// looks first: the user's Downloads folder, like any other download.
func defaultDBPath() string {
	return filepath.Join(defaultOutDir(), dbFileName)
}

// dbInfo describes a database file as the UI shows it.
type dbInfo struct {
	Path      string `json:"path"`
	OK        bool   `json:"ok"`
	Problem   string `json:"problem,omitempty"`
	Documents int64  `json:"documents,omitempty"`
	Schema    int    `json:"schema,omitempty"`
	// Suggestion: a usable database elsewhere (in Downloads) when this one is not.
	Suggestion string `json:"suggestion,omitempty"`
}

// openDB opens path read-only and checks that this program understands it.
// On success the caller owns the returned *sql.DB.
func openDB(path string) (*sql.DB, dbInfo, error) {
	info := dbInfo{Path: path}
	fail := func(msg string) (*sql.DB, dbInfo, error) {
		info.Problem = msg
		return nil, info, fmt.Errorf("%s", msg)
	}
	if path == "" {
		return fail("Nog geen database gekozen.")
	}
	fi, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return fail("Bestand niet gevonden.")
	case err != nil:
		return fail("Database niet leesbaar: " + err.Error())
	case fi.IsDir():
		return fail("Dit is een map, geen databasebestand.")
	}

	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return fail("Database niet te openen: " + err.Error())
	}
	db.SetMaxOpenConns(4)

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return fail("Dit is geen (geldige) SQLite-database.")
	}
	if version == 0 {
		var n int
		err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name IN ('document', 'spreker', 'sprekers_dekking')`).Scan(&n)
		if err != nil || n != 3 {
			db.Close()
			return fail("Dit bestand is geen sgdmeta-database (of hij is onvolledig).")
		}
		version = 1
	}
	info.Schema = version
	if version > maxSchema {
		db.Close()
		return fail("Deze database is nieuwer dan dit programma. Download een nieuwere versie van de app.")
	}
	if version < minSchema {
		db.Close()
		return fail("Deze database is te oud voor dit programma. Download hem opnieuw.")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM document").Scan(&info.Documents); err != nil {
		db.Close()
		return fail("Database is beschadigd: " + err.Error())
	}
	info.OK = true
	return db, info, nil
}

// readOnlyDSN builds a SQLite URI that opens path read-only. immutable=1
// tells SQLite the file never changes, so it needs no -wal/-shm files (the
// harvester writes in WAL mode) and works from read-only locations too.
func readOnlyDSN(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") { // Windows: C:/x → /C:/x
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro&immutable=1"}
	return u.String()
}

// option is one choice in a filter dropdown.
type option struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// doctypes lists the document types with how many documents each has.
func doctypes(db *sql.DB) ([]option, error) {
	rows, err := db.Query(`SELECT doctype, COUNT(*) FROM document
		WHERE doctype IS NOT NULL GROUP BY doctype ORDER BY doctype`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []option{}
	for rows.Next() {
		var o option
		if err := rows.Scan(&o.Value, &o.Count); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
