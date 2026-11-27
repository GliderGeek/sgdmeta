package main

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// fixtureDB writes a small database with the harvester's tables and
// returns its path.
func fixtureDB(t *testing.T, userVersion int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), dbFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE document (identifier TEXT PRIMARY KEY, doctype TEXT, title TEXT,
			vergaderjaar TEXT, datum TEXT, dossiernummer TEXT, preferred_url TEXT)`,
		`CREATE TABLE spreker (identifier TEXT, naam TEXT)`,
		`CREATE TABLE sprekers_dekking (doctype TEXT, vergaderjaar TEXT, documenten INT, met_sprekers INT)`,
		`INSERT INTO document VALUES
			('h-1', 'Handelingen', 'Vergadering', '1980-1981', '1980-10-01', NULL, 'https://x/h-1'),
			('k-1', 'Kamerstuk', 'Wet op iets', '1979-1980', '1980-01-02', '9952', 'https://x/k-1'),
			('k-2', 'Kamerstuk', 'Wet, met "aanhalingstekens"; en puntkomma', '1981-1982', NULL, '9952', NULL)`,
		fmt.Sprintf("PRAGMA user_version = %d", userVersion),
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func mustOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, info, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.OK || info.Documents != 3 || info.Schema != 1 {
		t.Fatalf("info = %+v", info)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ---------------------------------------------------------------- db

func TestOpenDB(t *testing.T) {
	mustOpen(t, fixtureDB(t, 0)) // first Zenodo release: no version, tables present
	mustOpen(t, fixtureDB(t, 1))

	newer := fixtureDB(t, maxSchema+1)
	if _, info, err := openDB(newer); err == nil || !strings.Contains(info.Problem, "nieuwer") {
		t.Errorf("newer schema: err=%v info=%+v", err, info)
	}

	junk := filepath.Join(t.TempDir(), "junk.sqlite3")
	os.WriteFile(junk, []byte("geen database"), 0o644)
	if _, _, err := openDB(junk); err == nil {
		t.Error("junk file accepted")
	}

	if _, info, err := openDB(filepath.Join(t.TempDir(), "nope.sqlite3")); err == nil || info.Problem != "Bestand niet gevonden." {
		t.Errorf("missing file: err=%v info=%+v", err, info)
	}
}

func TestReadOnlyDSNKeepsFileUntouched(t *testing.T) {
	path := fixtureDB(t, 1)
	db := mustOpen(t, path)
	if _, err := db.Exec("DELETE FROM document"); err == nil {
		t.Fatal("write succeeded on a read-only database")
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		t.Error("opening created a -wal file")
	}
}

func TestDoctypes(t *testing.T) {
	got, err := doctypes(mustOpen(t, fixtureDB(t, 1)))
	if err != nil {
		t.Fatal(err)
	}
	want := []option{{"Handelingen", 1}, {"Kamerstuk", 2}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSuggestsDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows
	os.MkdirAll(filepath.Join(home, "Downloads"), 0o755)
	src, _ := os.ReadFile(fixtureDB(t, 1))
	os.WriteFile(filepath.Join(home, "Downloads", dbFileName), src, 0o644)

	want := filepath.Join(home, "Downloads", dbFileName)
	s := &server{}
	info := s.useDB("", true)
	if info.OK || suggestion(info) != want {
		t.Errorf("nothing chosen: info = %+v, suggestion = %q", info, suggestion(info))
	}
	info = s.useDB(want, true)
	if !info.OK || suggestion(info) != "" {
		t.Errorf("Downloads chosen: info = %+v, suggestion = %q", info, suggestion(info))
	}
	s.db.Close()
}

// ---------------------------------------------------------------- query

func TestViewWhere(t *testing.T) {
	where, args, err := View{}.where()
	if err != nil || where != "" || args != nil {
		t.Errorf("empty view: %q %v %v", where, args, err)
	}

	where, args, err = View{Filters: []Filter{{"doctype", "eq", "Kamerstuk"}}}.where()
	if err != nil || where != " WHERE d.doctype = ?" || fmt.Sprint(args) != "[Kamerstuk]" {
		t.Errorf("doctype: %q %v %v", where, args, err)
	}

	for _, f := range []Filter{
		{"title; DROP TABLE document", "eq", "x"}, // unknown field
		{"doctype", "like", "x"},                  // unsupported operator
	} {
		if _, _, err := (View{Filters: []Filter{f}}).where(); err == nil {
			t.Errorf("accepted %+v", f)
		}
	}
}

func TestViewRows(t *testing.T) {
	db := mustOpen(t, fixtureDB(t, 1))
	v := View{Filters: []Filter{{"doctype", "eq", "Kamerstuk"}}}
	n, err := v.count(db)
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}
	var ids []string
	err = v.eachRow(db, 0, func(rec []string) error { ids = append(ids, rec[0]); return nil })
	if err != nil || fmt.Sprint(ids) != "[k-1 k-2]" { // ordered by vergaderjaar
		t.Errorf("rows = %v, %v", ids, err)
	}
}

func TestOutBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "sgdmeta-export"},
		{"lijst", "lijst"},
		{"lijst.XLSX", "lijst"},
		{".xlsx", "sgdmeta-export"},
		{"../../etc/lijst", "lijst"},
	}
	for _, c := range cases {
		if got := outBase(c.in); got != c.want {
			t.Errorf("outBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- export

func TestExportXLSX(t *testing.T) {
	db := mustOpen(t, fixtureDB(t, 1))
	out := filepath.Join(t.TempDir(), "x.xlsx")
	if err := writeExport(db, View{}, out, 3, nil); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenFile(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(sheetName)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0][0] != "Identifier" || rows[1][0] != "k-1" {
		t.Errorf("rows = %v", rows)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".sgdmeta-export-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}
}

func TestExportTooManyRowsForExcel(t *testing.T) {
	db := mustOpen(t, fixtureDB(t, 1))
	out := filepath.Join(t.TempDir(), "x.xlsx")
	err := writeExport(db, View{}, out, excelMaxRows, nil)
	if err == nil || !strings.Contains(err.Error(), "kleiner") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("file written anyway")
	}
}

// ---------------------------------------------------------------- download

// fakeZenodo serves a record and its file, with Range support, and records
// the Range headers it received.
func fakeZenodo(t *testing.T, content []byte, checksum string) *[]string {
	t.Helper()
	var mu sync.Mutex
	ranges := []string{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/api/records/1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"files":[{"key":"plan.json","size":1,"checksum":"md5:00","links":{"self":"x"}},
			{"key":%q,"size":%d,"checksum":"md5:%s","links":{"self":"%s/file"}}]}`,
			dbFileName, len(content), checksum, srv.URL)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		http.ServeContent(w, r, "f", time.Time{}, strings.NewReader(string(content)))
	})
	old := zenodoRecordURL
	zenodoRecordURL = srv.URL + "/api/records/1"
	t.Cleanup(func() { zenodoRecordURL = old })
	return &ranges
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

func TestDownloadResumes(t *testing.T) {
	content := []byte(strings.Repeat("sgdmeta ", 100_000))
	ranges := fakeZenodo(t, content, md5hex(content))
	dest := filepath.Join(t.TempDir(), "sub", dbFileName)
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(dest+".part", content[:300_000], 0o644) // an interrupted earlier run

	var last int64
	err := downloadDB(context.Background(), dest, func(_ string, done, _ int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(content) {
		t.Error("content differs")
	}
	if fmt.Sprint(*ranges) != "[bytes=300000-]" {
		t.Errorf("ranges = %v", *ranges)
	}
	if last != int64(len(content)) {
		t.Errorf("last progress = %d", last)
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Error(".part left behind")
	}
}

func TestDownloadRejectsWrongChecksum(t *testing.T) {
	content := []byte("inhoud")
	fakeZenodo(t, content, md5hex([]byte("iets anders")))
	dest := filepath.Join(t.TempDir(), dbFileName)
	err := downloadDB(context.Background(), dest, func(string, int64, int64) {})
	if err == nil || !strings.Contains(err.Error(), "beschadigd") {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists", p)
		}
	}
}
