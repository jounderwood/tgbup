// Package serve is tgbup's web interface on this computer: the state of the
// archive and its backups, a run on demand, the chat list, and the archive
// itself as a site. Everything it does goes through a Backend, so the same
// handlers work with the real archive and with fakes in tests.
package serve

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrBusy means a run (manual or scheduled) is already in progress.
var ErrBusy = errors.New("a run is already in progress")

// Backend is what the interface shows and changes.
type Backend interface {
	Status(ctx context.Context) (Status, error)
	StartRun() error
	RunLog() (string, error)
	Chats(ctx context.Context) (ChatList, error)
	Counts(ctx context.Context, ids []int64) (map[int64]Count, error)
	Topics(ctx context.Context, chat int64) ([]Topic, error)
	Preview(ctx context.Context, sel Selection) (Preview, error)
	Save(ctx context.Context, sel Selection) error
}

type Status struct {
	Host     string    `json:"host"`
	Now      time.Time `json:"now"`
	Archive  Archive   `json:"archive"`
	Running  bool      `json:"running"`
	Runs     []Run     `json:"runs"` // newest first
	Backups  []Backup  `json:"backups"`
	Schedule Schedule  `json:"schedule"`
	Bot      bool      `json:"bot"`
	// Problems are setup steps still missing, each with what to do.
	Problems []string `json:"problems"`
}

type Archive struct {
	Dir          string `json:"dir"`
	Chats        int    `json:"chats"`
	Messages     int    `json:"messages"`
	Files        int    `json:"files"`
	PendingFiles int    `json:"pending_files"`
	FailedFiles  int    `json:"failed_files"`
	Bytes        int64  `json:"bytes"`
	// Site is true once html/index.html exists.
	Site bool `json:"site"`
}

type Run struct {
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished"`
	OK          bool      `json:"ok"`
	NewMessages int       `json:"new_messages"`
	NewMedia    int       `json:"new_media"`
	Errors      []string  `json:"errors"`
}

type Backup struct {
	Repo        string    `json:"repo"`
	LastSuccess time.Time `json:"last_success"` // zero: never
}

type Schedule struct {
	Installed bool      `json:"installed"`
	Times     []string  `json:"times"`
	Next      time.Time `json:"next"`
	// Note explains a schedule that cannot be installed here (not macOS).
	Note string `json:"note,omitempty"`
}

type ChatList struct {
	Groups []Group `json:"groups"`
	Chats  []Chat  `json:"chats"`
}

type Group struct {
	Title string  `json:"title"`
	IDs   []int64 `json:"ids"`
}

type Chat struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Type           string `json:"type"`
	Username       string `json:"username,omitempty"`
	Selected       bool   `json:"selected"`
	ExcludedTopics []int  `json:"excluded_topics"`
}

type Count struct {
	Messages int    `json:"messages"`
	Topics   int    `json:"topics"`
	Err      string `json:"error,omitempty"`
}

type Topic struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Messages int    `json:"messages"`
}

// Selection is the chat list to save: chat ids and, per forum, excluded topics.
// Telegram ids fit in 52 bits, so JavaScript numbers carry them exactly.
type Selection struct {
	Chats         []int64         `json:"chats"`
	ExcludeTopics map[int64][]int `json:"exclude_topics"`
}

type Preview struct {
	Report string `json:"report"`
	Config string `json:"config"`
}

// Server serves the interface on a loopback address.
type Server struct {
	backend    Backend
	archiveDir string
	token      string
	port       string
	pages      *template.Template
}

//go:embed web
var webFS embed.FS

// CheckListen accepts only loopback addresses: the archive is private and the
// interface has no login.
func CheckListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen %q: only this computer (127.0.0.1, localhost) is allowed: the interface has no login", addr)
	}
	return nil
}

// New builds a server for an address already bound to port.
func New(b Backend, archiveDir, port string) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	pages, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{backend: b, archiveDir: archiveDir, token: hex.EncodeToString(tok), port: port, pages: pages}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page("overview.html"))
	mux.HandleFunc("GET /chats", s.page("chats.html"))
	static, _ := fs.Sub(webFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /archive/{dir}/{path...}", s.archive)

	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/run", s.run)
	mux.HandleFunc("GET /api/run/log", s.runLog)
	mux.HandleFunc("GET /api/chats", s.chats)
	mux.HandleFunc("POST /api/chats/counts", s.counts)
	mux.HandleFunc("GET /api/chats/{id}/topics", s.topics)
	mux.HandleFunc("POST /api/chats/preview", s.preview)
	mux.HandleFunc("POST /api/chats", s.save)
	return s.guard(mux)
}

// guard rejects requests a page from another site could make: a foreign Host
// (DNS rebinding) and changes without the token only our pages carry.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || port != s.port || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Tgbup-Token") != s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		if err := s.pages.ExecuteTemplate(w, name, map[string]string{"Token": s.token}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// archive serves html/ and media/ of the archive and nothing else from it.
func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	dir := r.PathValue("dir")
	if dir != "html" && dir != "media" {
		http.NotFound(w, r)
		return
	}
	rel := path.Clean("/" + r.PathValue("path"))
	if strings.HasSuffix(r.URL.Path, "/") && rel != "/" {
		rel += "/"
	}
	full := filepath.Join(s.archiveDir, dir, filepath.FromSlash(rel))
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err == nil && st.IsDir() {
		f.Close()
		if f, err = os.Open(filepath.Join(full, "index.html")); err == nil {
			defer f.Close()
			st, err = f.Stat()
		}
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// ServeContent, not ServeFile: no redirects for index.html, no directory listings.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.backend.Status(r.Context())
	reply(w, st, err)
}

func (s *Server) run(w http.ResponseWriter, _ *http.Request) {
	err := s.backend.StartRun()
	if errors.Is(err, ErrBusy) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) runLog(w http.ResponseWriter, _ *http.Request) {
	text, err := s.backend.RunLog()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, text)
}

func (s *Server) chats(w http.ResponseWriter, r *http.Request) {
	list, err := s.backend.Chats(r.Context())
	reply(w, list, err)
}

func (s *Server) counts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	got, err := s.backend.Counts(r.Context(), req.IDs)
	reply(w, got, err)
}

func (s *Server) topics(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad chat id", http.StatusBadRequest)
		return
	}
	ts, err := s.backend.Topics(r.Context(), id)
	reply(w, ts, err)
}

func (s *Server) selection(w http.ResponseWriter, r *http.Request) (Selection, bool) {
	var sel Selection
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&sel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return sel, false
	}
	if len(sel.Chats) == 0 {
		http.Error(w, "choose at least one chat", http.StatusBadRequest)
		return sel, false
	}
	return sel, true
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.selection(w, r)
	if !ok {
		return
	}
	p, err := s.backend.Preview(r.Context(), sel)
	reply(w, p, err)
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.selection(w, r)
	if !ok {
		return
	}
	if err := s.backend.Save(r.Context(), sel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// Serve runs the server on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
