// Browser UI: a small HTTP server on 127.0.0.1 serves web/index.html and a
// JSON API; the default browser is opened on it. The process exits when the
// page has been gone for a while (or a newer launch replaces it), but never
// during a download or export.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

const (
	pingTimeout  = 3 * time.Minute  // page gone (browsers throttle background tabs, so be generous)
	startTimeout = 2 * time.Minute  // browser never opened the page
	byeGrace     = 10 * time.Second // after the tab closes, wait for a possible reload

	previewRows = 10
)

// ---------------------------------------------------------------- settings

type settings struct {
	DBPath  string `json:"dbPath"`
	OutDir  string `json:"outDir"`
	OutName string `json:"outName"`
	View    View   `json:"view"`
}

func configDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, appName)
}

func settingsPath() string {
	if d := configDir(); d != "" {
		return filepath.Join(d, "settings.json")
	}
	return ""
}

func defaultOutDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
		return filepath.Join(home, "Downloads")
	}
	return home
}

func loadSettings() settings {
	s := settings{}
	if p := settingsPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &s)
		}
	}
	if s.DBPath == "" {
		s.DBPath = defaultDBPath()
	}
	if s.OutDir == "" {
		s.OutDir = defaultOutDir()
	}
	s.OutName = outBase(s.OutName) // also strips .xlsx from older settings
	if s.View.Filters == nil {
		s.View.Filters = []Filter{}
	}
	return s
}

func saveSettings(s settings) {
	p := settingsPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(p, b, 0o644)
}

// ---------------------------------------------------------------- server

// task is a long-running job (download or export) as the page sees it.
type task struct {
	Running  bool   `json:"running"`
	Done     bool   `json:"done"`
	Stage    string `json:"stage,omitempty"`
	Progress int64  `json:"progress"`
	Total    int64  `json:"total"`
	Error    string `json:"error,omitempty"`
	Summary  string `json:"summary,omitempty"`

	out    string
	cancel context.CancelFunc
}

type server struct {
	token    string
	quit     chan struct{}
	quitOnce sync.Once

	mu       sync.Mutex
	settings settings
	db       *sql.DB
	dbInfo   dbInfo
	download task
	export   task
	lastPing time.Time
	byeAt    time.Time
}

func runGUI(dbFlag string) {
	if detachFromLauncher() {
		return
	}
	// Already running? Then just show that page again.
	if url := runningInstance(); url != "" {
		openBrowser(url)
		return
	}

	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	s := &server{token: hex.EncodeToString(tok), quit: make(chan struct{}), settings: loadSettings()}
	if dbFlag != "" {
		s.settings.DBPath = dbFlag
	}
	if info := s.useDB(s.settings.DBPath, false); !info.OK && dbFlag == "" {
		s.useDB(defaultDBPath(), false) // chosen file is gone: try Downloads
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "FOUT:", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr(), s.token)
	writeInstance(url)
	defer removeInstance(url)

	go http.Serve(ln, s.routes())
	fmt.Println("sgdmeta draait op", url)
	fmt.Println("Sluit dit venster of het browsertabblad om te stoppen.")
	openBrowser(url)
	s.watchdog()
	s.mu.Lock()
	if s.download.cancel != nil {
		s.download.cancel()
	}
	s.mu.Unlock()
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(iconPNG)
	})
	s.api(mux, "/api/state", s.handleState)
	s.api(mux, "/api/settings", s.handleSettings)
	s.api(mux, "/api/pick", s.handlePick)
	s.api(mux, "/api/db/use", s.handleUseDB)
	s.api(mux, "/api/download/start", s.handleDownloadStart)
	s.api(mux, "/api/download/status", s.status(&s.download))
	s.api(mux, "/api/download/cancel", s.handleDownloadCancel)
	s.api(mux, "/api/meta", s.handleMeta)
	s.api(mux, "/api/preview", s.handlePreview)
	s.api(mux, "/api/export/start", s.handleExportStart)
	s.api(mux, "/api/export/status", s.status(&s.export))
	s.api(mux, "/api/open", func(*http.Request) (any, error) { return nil, openFile(s.exportPath()) })
	s.api(mux, "/api/reveal", func(*http.Request) (any, error) { return nil, revealFile(s.exportPath()) })
	s.api(mux, "/api/ping", s.handlePing)
	s.api(mux, "/api/bye", s.handleBye)
	s.api(mux, "/api/quit", func(*http.Request) (any, error) {
		go func() { time.Sleep(300 * time.Millisecond); s.quitOnce.Do(func() { close(s.quit) }) }()
		return nil, nil
	})
	return mux
}

// ---------------------------------------------------------------- single instance

func instancePath() string {
	if d := configDir(); d != "" {
		return filepath.Join(d, "instance.txt")
	}
	return ""
}

// selfID identifies this exact program file, so a different copy or a
// newer build is not mistaken for "the same program".
func selfID() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return exe
	}
	return fmt.Sprintf("%s|%d|%d", exe, fi.Size(), fi.ModTime().UnixNano())
}

// postInstance calls an API endpoint of another running instance.
func postInstance(url, path string) bool {
	i := strings.Index(url, "/?t=")
	if i < 0 {
		return false
	}
	req, err := http.NewRequest(http.MethodPost, url[:i]+path, strings.NewReader("{}"))
	if err != nil {
		return false
	}
	req.Header.Set("X-Token", url[i+len("/?t="):])
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// runningInstance returns the page URL of this same program if it is
// already running. A different copy or version that is running is asked to
// stop, so the one the user just started takes over.
func runningInstance() string {
	p := instancePath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	url, id := lines[0], ""
	if len(lines) == 2 {
		id = strings.TrimSpace(lines[1])
	}
	if !postInstance(url, "/api/ping") {
		return ""
	}
	if id != "" && id == selfID() {
		return url
	}
	postInstance(url, "/api/quit")
	for i := 0; i < 20 && postInstance(url, "/api/ping"); i++ {
		time.Sleep(150 * time.Millisecond)
	}
	return ""
}

func writeInstance(url string) {
	if p := instancePath(); p != "" {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(url+"\n"+selfID()+"\n"), 0o600)
	}
}

func removeInstance(url string) {
	p := instancePath()
	if b, err := os.ReadFile(p); err == nil && strings.SplitN(string(b), "\n", 2)[0] == url {
		_ = os.Remove(p)
	}
}

func openBrowser(url string) {
	if os.Getenv("SGDMETA_NO_BROWSER") != "" { // for testing
		return
	}
	if err := openFile(url); err != nil {
		fmt.Fprintln(os.Stderr, "Kon de browser niet openen; open zelf:", url)
	}
}

// watchdog blocks until the program should exit.
func (s *server) watchdog() {
	started := time.Now()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
		}
		s.mu.Lock()
		busy := s.download.Running || s.export.Running
		last, bye := s.lastPing, s.byeAt
		s.mu.Unlock()
		if busy {
			continue
		}
		now := time.Now()
		switch {
		case last.IsZero() && now.Sub(started) > startTimeout,
			!last.IsZero() && now.Sub(last) > pingTimeout,
			!bye.IsZero() && last.Before(bye) && now.Sub(bye) > byeGrace:
			return
		}
	}
}

// api registers a JSON endpoint protected by the session token. The token
// keeps other web pages in the browser from talking to this local server.
func (s *server) api(mux *http.ServeMux, path string, fn func(*http.Request) (any, error)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Token")
		if tok == "" {
			tok = r.URL.Query().Get("t") // sendBeacon cannot set headers
		}
		if tok != s.token || r.Method != http.MethodPost {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		res, err := fn(r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if res == nil {
			res = map[string]any{}
		}
		json.NewEncoder(w).Encode(res)
	})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("ongeldig verzoek: %w", err)
	}
	return nil
}

func (s *server) status(t *task) func(*http.Request) (any, error) {
	return func(*http.Request) (any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return *t, nil
	}
}

func (s *server) handlePing(*http.Request) (any, error) {
	s.mu.Lock()
	s.lastPing = time.Now()
	s.mu.Unlock()
	return nil, nil
}

func (s *server) handleBye(*http.Request) (any, error) {
	s.mu.Lock()
	s.byeAt = time.Now()
	s.mu.Unlock()
	return nil, nil
}

// ---------------------------------------------------------------- state & settings

func (s *server) handleState(*http.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"settings": s.settings,
		"db":       withSuggestion(s.dbInfo),
		"download": s.download,
		"export":   s.export,
	}, nil
}

// handleSettings stores what the page edits. The database path is not among
// it: that changes only through useDB.
func (s *server) handleSettings(r *http.Request) (any, error) {
	var in settings
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	in.DBPath = s.settings.DBPath
	if in.View.Filters == nil {
		in.View.Filters = []Filter{}
	}
	s.settings = in
	s.mu.Unlock()
	saveSettings(in)
	return nil, nil
}

func (s *server) handlePick(r *http.Request) (any, error) {
	var in struct {
		Prompt string `json:"prompt"`
		Ext    string `json:"ext"` // set: choose a file with this extension, else a folder
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	var p string
	var err error
	if in.Ext != "" {
		p, err = pickFileDialog(in.Prompt, in.Ext)
	} else {
		p, err = pickFolderDialog(in.Prompt)
	}
	switch {
	case err == nil:
		return map[string]any{"path": filepath.Clean(p)}, nil
	case errors.Is(err, errCancelled):
		return map[string]any{"cancelled": true}, nil
	default:
		return map[string]any{"unavailable": true}, nil
	}
}

// ---------------------------------------------------------------- database

// useDB switches to the database at path (a file, or a folder containing
// sgdmeta.sqlite3). If path is not usable, the previous one stays in use,
// unless replace is set: then nothing is in use any more (the user chose
// another location, and the page shows that one).
func (s *server) useDB(path string, replace bool) dbInfo {
	path = strings.Trim(strings.TrimSpace(path), `"'`)
	if abs, err := filepath.Abs(path); err == nil && path != "" {
		path = abs // settings outlive the working directory
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = filepath.Join(path, dbFileName)
	}
	db, info, err := openDB(path)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if replace && s.db != nil {
			s.db.Close()
			s.db = nil
		}
		if s.db == nil {
			s.dbInfo = info
			s.settings.DBPath = path
		}
		return info
	}
	if s.db != nil {
		s.db.Close()
	}
	s.db, s.dbInfo = db, info
	s.settings.DBPath = path
	return info
}

func (s *server) handleUseDB(r *http.Request) (any, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if s.busy() {
		return nil, errors.New("wacht tot de download of export klaar is")
	}
	info := s.useDB(in.Path, true)
	info.Suggestion = suggestion(info)
	if info.OK {
		s.mu.Lock()
		saveSettings(s.settings)
		s.mu.Unlock()
	}
	return info, nil
}

// suggestion returns the database in the Downloads folder if info is not
// usable and that one is, so the page can offer it with one click.
func suggestion(info dbInfo) string {
	if info.OK {
		return ""
	}
	d := defaultDBPath()
	if d == info.Path {
		return ""
	}
	if _, err := os.Stat(d); err != nil {
		return ""
	}
	db, _, err := openDB(d)
	if err != nil {
		return ""
	}
	db.Close()
	return d
}

func withSuggestion(info dbInfo) dbInfo {
	info.Suggestion = suggestion(info)
	return info
}

func (s *server) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.download.Running || s.export.Running
}

func (s *server) currentDB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("er is nog geen database")
	}
	return s.db, nil
}

// handleDownloadStart downloads the database to the default location (the
// Downloads folder) and uses it when it is complete.
func (s *server) handleDownloadStart(*http.Request) (any, error) {
	dest := defaultDBPath()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.download.Running || s.export.Running {
		return nil, errors.New("er loopt al een download of export")
	}
	if s.db != nil && s.dbInfo.Path == dest { // being replaced: release the file
		s.db.Close()
		s.db, s.dbInfo = nil, dbInfo{Path: dest, Problem: "Wordt opnieuw gedownload."}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.download = task{Running: true, Stage: "verbinden", out: dest, cancel: cancel}

	go func() {
		err := downloadDB(ctx, dest, func(stage string, done, total int64) {
			s.mu.Lock()
			s.download.Stage, s.download.Progress, s.download.Total = stage, done, total
			s.mu.Unlock()
		})
		var info dbInfo
		if err == nil {
			info = s.useDB(dest, false)
			if info.OK {
				s.mu.Lock()
				saveSettings(s.settings)
				s.mu.Unlock()
			} else {
				err = errors.New(info.Problem)
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.download.Running, s.download.Done, s.download.cancel = false, true, nil
		switch {
		case errors.Is(err, errCancelled):
			s.download.Error = "Gestopt. Opnieuw starten gaat verder waar hij was."
		case err != nil:
			s.download.Error = err.Error()
		default:
			s.download.Summary = fmt.Sprintf("Klaar: %d documenten.", info.Documents)
		}
	}()
	return map[string]any{"started": true, "path": dest}, nil
}

func (s *server) handleDownloadCancel(*http.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.download.cancel != nil {
		s.download.cancel()
	}
	return nil, nil
}

// ---------------------------------------------------------------- view

func (s *server) handleMeta(*http.Request) (any, error) {
	db, err := s.currentDB()
	if err != nil {
		return nil, err
	}
	types, err := doctypes(db)
	if err != nil {
		return nil, err
	}
	return map[string]any{"doctypes": types, "columns": columns}, nil
}

func (s *server) handlePreview(r *http.Request) (any, error) {
	var in struct {
		View View `json:"view"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	db, err := s.currentDB()
	if err != nil {
		return nil, err
	}
	n, err := in.View.count(db)
	if err != nil {
		return nil, err
	}
	rows := [][]string{}
	err = in.View.eachRow(db, previewRows, func(rec []string) error {
		rows = append(rows, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n, "rows": rows, "excelMax": excelMaxRows - 1}, nil
}

func (s *server) exportPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.export.out
}

func (s *server) handleExportStart(r *http.Request) (any, error) {
	var in struct {
		settings
		Overwrite bool `json:"overwrite"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	dir := filepath.Clean(strings.Trim(strings.TrimSpace(in.OutDir), `"'`))
	if fi, err := os.Stat(dir); in.OutDir == "" || err != nil || !fi.IsDir() {
		return nil, errors.New("kies een bestaande map om de export in op te slaan")
	}
	base := outBase(in.OutName)
	name := base + ".xlsx"
	out := filepath.Join(dir, name)
	if _, err := os.Stat(out); err == nil && !in.Overwrite {
		return map[string]any{"exists": true, "outName": name}, nil
	}
	if _, _, err := in.View.where(); err != nil {
		return nil, err
	}
	db, err := s.currentDB()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.download.Running || s.export.Running {
		s.mu.Unlock()
		return nil, errors.New("er loopt al een download of export")
	}
	s.settings = settings{DBPath: s.settings.DBPath, OutDir: dir, OutName: base, View: in.View}
	s.export = task{Running: true, Stage: "tellen", out: out}
	saveSettings(s.settings)
	s.mu.Unlock()

	go func() {
		total, err := in.View.count(db)
		if err == nil {
			s.mu.Lock()
			s.export.Stage, s.export.Total = "schrijven", total
			s.mu.Unlock()
			err = writeExport(db, in.View, out, total, func(n int64) {
				s.mu.Lock()
				s.export.Progress = n
				s.mu.Unlock()
			})
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.export.Running, s.export.Done = false, true
		if err != nil {
			s.export.Error = err.Error()
			return
		}
		s.export.Progress = total
		s.export.Summary = "Opgeslagen als " + name + "."
	}()
	return map[string]any{"started": true, "outName": base}, nil
}
