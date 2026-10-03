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
	"log/slog"
	"mime"
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

// ErrBadRequest marks errors caused by what the page sent.
var ErrBadRequest = errors.New("bad request")

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
	// SetupState and SetupAction drive the setup page: the steps' state, and
	// one step's action with the form the page sent (JSON in, JSON out).
	SetupState(ctx context.Context) (any, error)
	SetupAction(ctx context.Context, name string, body []byte) (any, error)
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
	// Warnings name configured chats not in the list: saving drops them.
	Warnings []string `json:"warnings"`
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
	// Log, when set, gets a line for each action taken on the page and each
	// failed request; request bodies (passwords, codes, tokens) never.
	Log         *slog.Logger
	backend     Backend
	archiveDir  func() string
	token       string
	port        string
	archivePort string
	pages       *template.Template
}

//go:embed web
var webFS embed.FS

// CheckListen accepts only this computer's addresses that the Host check
// also accepts: the archive is private and the interface has no login.
func CheckListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen %q: %w", addr, err)
	}
	if !localHost(host) {
		return fmt.Errorf("--listen %q: only this computer (127.0.0.1, ::1, localhost) is allowed: the interface has no login", addr)
	}
	return nil
}

func localHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// New builds a server for addresses already bound to port (the interface)
// and archivePort (the archive as a site). archiveDir is asked on every
// request, as tgbup init may move the archive meanwhile.
//
// The archive gets its own origin because it holds files other people sent:
// a page among them that runs a script there can neither read the interface
// (and its token) nor call the API, which needs the token.
func New(b Backend, archiveDir func() string, port, archivePort string) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	pages, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{backend: b, archiveDir: archiveDir, token: hex.EncodeToString(tok), port: port, archivePort: archivePort, pages: pages}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page("overview.html"))
	mux.HandleFunc("GET /chats", s.page("chats.html"))
	mux.HandleFunc("GET /setup", s.page("setup.html"))
	static, _ := fs.Sub(webFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/run", s.run)
	mux.HandleFunc("GET /api/run/log", s.runLog)
	mux.HandleFunc("GET /api/chats", s.chats)
	mux.HandleFunc("POST /api/chats/counts", s.counts)
	mux.HandleFunc("GET /api/chats/{id}/topics", s.topics)
	mux.HandleFunc("POST /api/chats/preview", s.preview)
	mux.HandleFunc("POST /api/chats", s.save)
	mux.HandleFunc("GET /api/setup", s.setupState)
	mux.HandleFunc("POST /api/setup/{action...}", s.setupAction)
	return s.logged(s.guard(mux, s.port, true))
}

// slowRequest is how long a read takes before the log mentions it.
const slowRequest = 2 * time.Second

// logged reports actions (POST) when they start and end, and any request
// that failed, with the error the page was given.
func (s *Server) logged(next http.Handler) http.Handler {
	if s.Log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Method == http.MethodPost
		if action {
			s.Log.Info("page: " + r.URL.Path + " …")
		}
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		took := time.Since(start).Round(time.Millisecond)
		api := strings.HasPrefix(r.URL.Path, "/api/")
		switch {
		case rec.status >= 500 || rec.status >= 400 && api:
			s.Log.Warn("page: "+r.Method+" "+r.URL.Path+" failed", "status", rec.status, "error", strings.TrimSpace(rec.errText.String()), "took", took)
		case action:
			s.Log.Info("page: "+r.URL.Path+" done", "took", took)
		case api && took > slowRequest:
			s.Log.Info("page: "+r.URL.Path+" was slow", "took", took)
		}
	})
}

// recorder keeps the status and, for a failure, the start of the message
// sent to the page.
type recorder struct {
	http.ResponseWriter
	status  int
	errText strings.Builder
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status >= 400 && r.errText.Len() < 500 {
		r.errText.Write(b[:min(len(b), 500-r.errText.Len())])
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ArchiveHandler serves html/ and media/ of the archive on its own origin.
func (s *Server) ArchiveHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.RedirectHandler("/html/", http.StatusFound))
	mux.HandleFunc("GET /{dir}/{path...}", s.archive)
	return s.guard(mux, s.archivePort, false)
}

// guard rejects what a page from another site could make the browser do: a
// foreign Host (DNS rebinding), any API call without the token only our pages
// carry (a custom header, so the browser asks first and gets no CORS), and
// loading our pages as a subresource.
func (s *Server) guard(next http.Handler, wantPort string, ui bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || port != wantPort || !localHost(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if ui && strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("X-Tgbup-Token") != s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Other sites may link here but not load anything: the archive's search
		// indexes are scripts any page could include to read the messages, and
		// the archive's origin may not load the interface either.
		if r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin" &&
			r.Header.Get("Sec-Fetch-Site") != "none" && r.Header.Get("Sec-Fetch-Mode") != "navigate" {
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
		host, _, _ := net.SplitHostPort(r.Host)
		archive := "http://" + net.JoinHostPort(host, s.archivePort) + "/html/"
		if err := s.pages.ExecuteTemplate(w, name, map[string]string{"Token": s.token, "Archive": archive}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// archive serves html/ and media/ of the archive and nothing else from it.
// A received file opened by itself runs sandboxed, and anything but a
// picture, video or sound is downloaded rather than shown.
func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	dir := r.PathValue("dir")
	if dir != "html" && dir != "media" {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(filepath.Join(s.archiveDir(), dir))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	// OpenRoot also refuses symlinks leading out of the folder.
	rel := strings.TrimPrefix(path.Clean("/"+r.PathValue("path")), "/")
	if rel == "" {
		rel = "."
	}
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err == nil && st.IsDir() {
		f.Close()
		if f, err = root.Open(filepath.Join(filepath.FromSlash(rel), "index.html")); err == nil {
			defer f.Close()
			st, err = f.Stat()
		}
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if dir == "media" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
		if !inlineMedia(st.Name()) {
			w.Header().Set("Content-Disposition", "attachment")
		}
	}
	// ServeContent, not ServeFile: no redirects for index.html, no directory listings.
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// inlineMedia reports files a browser shows without running anything in them.
func inlineMedia(name string) bool {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if t == "" || strings.HasPrefix(t, "image/svg") {
		return false
	}
	return strings.HasPrefix(t, "image/") || strings.HasPrefix(t, "video/") || strings.HasPrefix(t, "audio/")
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
		replyErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setupState(w http.ResponseWriter, r *http.Request) {
	st, err := s.backend.SetupState(r.Context())
	reply(w, st, err)
}

func (s *Server) setupAction(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.backend.SetupAction(r.Context(), r.PathValue("action"), body)
	if err == nil && res == nil {
		res = map[string]bool{"ok": true}
	}
	reply(w, res, err)
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		replyErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func replyErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, ErrBadRequest) {
		code = http.StatusBadRequest
	}
	http.Error(w, err.Error(), code)
}

// Serve runs the interface on ln and the archive on archiveLn until ctx ends.
func (s *Server) Serve(ctx context.Context, ln, archiveLn net.Listener) error {
	errs := make(chan error, 2)
	for _, p := range []struct {
		ln net.Listener
		h  http.Handler
	}{{ln, s.Handler()}, {archiveLn, s.ArchiveHandler()}} {
		// Requests share ctx, so a shutdown also ends the ones waiting on Telegram.
		srv := &http.Server{Handler: p.h, ReadHeaderTimeout: 10 * time.Second,
			BaseContext: func(net.Listener) context.Context { return ctx }}
		go func() {
			<-ctx.Done()
			shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shut)
		}()
		go func() {
			if err := srv.Serve(p.ln); !errors.Is(err, http.ErrServerClosed) {
				errs <- err
				return
			}
			errs <- nil
		}()
	}
	return errors.Join(<-errs, <-errs)
}
