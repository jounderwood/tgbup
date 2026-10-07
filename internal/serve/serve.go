// Package serve is tgbup's web interface on this computer: the state of the
// archive and its backups, a run on demand, and the setup with the chat list.
// The archive's pages are files opened straight from the disk. Everything it does goes through a Backend, so the same
// handlers work with the real archive and with fakes in tests.
package serve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
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
	// StartRun starts tgbup run; takeout for the first, full download.
	StartRun(takeout bool) error
	RunLog() (string, error)
	Chats(ctx context.Context) (ChatList, error)
	Counts(ctx context.Context, ids []int64) (map[int64]Count, error)
	// Topics lists a forum's topics; counting their messages takes a request
	// per topic, so it happens only when asked (counts).
	Topics(ctx context.Context, chat int64, counts bool) ([]Topic, error)
	// Preview shows what Save would write; the per-chat message counts, which
	// take several requests per chat, only when asked (counts).
	Preview(ctx context.Context, sel Selection, counts bool) (Preview, error)
	Save(ctx context.Context, sel Selection) error
	// SetupState and SetupAction drive the setup page: the steps' state, and
	// one step's action with the form the page sent (JSON in, JSON out).
	SetupState(ctx context.Context) (any, error)
	SetupAction(ctx context.Context, name string, body []byte) (any, error)
	// OpenArchive shows the archive's pages in the default browser.
	OpenArchive() error
	// Snapshots lists the snapshots in the place-th backup place of Status.
	Snapshots(ctx context.Context, place int) ([]Snapshot, error)
	// RestoreCopy restores a snapshot of the place (by the id Snapshots gave)
	// into a folder of its own next to the archive; the archive is not touched.
	RestoreCopy(ctx context.Context, place int, id string) (Snapshot, error)
	// OpenCopy shows a restored copy's pages in the default browser.
	OpenCopy(place int, id string) error
}

type Snapshot struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Host string    `json:"host"`
	// Changes is what changed in the archive since the snapshot before; ""
	// for a snapshot tgbup has no record of.
	Changes string `json:"changes"`
	// Command restores it into Copy, a folder of its own; Restored: it is there.
	Command  string `json:"command"`
	Copy     string `json:"copy"`
	Restored bool   `json:"restored"`
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
	// SetupLeft names the required settings not made yet: until then the
	// archive is not being kept.
	SetupLeft []string `json:"setup_left"`
	// Problems are other things to fix (an unencrypted disk).
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
	// Snapshot says what the backups did: "snapshot 3333bbbb", "unchanged,
	// no new snapshot", or "" when there are none.
	Snapshot string   `json:"snapshot"`
	Errors   []string `json:"errors"`
	// Log is the run's warnings and errors as logged.
	Log []string `json:"log"`
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
	ID    int    `json:"id"`
	Title string `json:"title"`
	// Messages is -1 until counted.
	Messages int `json:"messages"`
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
	Log     *slog.Logger
	backend Backend
	// key opens the interface: it is in the address tgbup prints and opens,
	// and is exchanged for a cookie. Without it a page is refused, so another
	// program or account on this computer cannot read token off a page.
	key string
	// token authorizes the API, in a header only our pages send; it is also
	// the cookie's value.
	token string
	port  string
	pages   *template.Template
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

// New builds a server for an address already bound to port.
func New(b Backend, port string) (*Server, error) {
	tok, key := make([]byte, 16), make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	pages, err := template.ParseFS(webFS, "web/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{backend: b, token: hex.EncodeToString(tok), key: hex.EncodeToString(key), port: port, pages: pages}, nil
}

// Key is what an address needs (?key=…) to open the interface in a browser.
func (s *Server) Key() string { return s.key }

const cookieName = "tgbup"

// signedIn: the request carries the cookie, or the key, which then sets it.
func (s *Server) signedIn(w http.ResponseWriter, r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1 {
		return true
	}
	if k := r.URL.Query().Get("key"); k != "" && subtle.ConstantTimeCompare([]byte(k), []byte(s.key)) == 1 {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		return true
	}
	return false
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page("overview.html"))
	// The chat list is a step of Setup.
	// Settings were called Setup, and the chat list was a page of its own.
	mux.Handle("GET /chats", http.RedirectHandler("/settings#chats", http.StatusFound))
	mux.Handle("GET /setup", http.RedirectHandler("/settings", http.StatusFound))
	mux.HandleFunc("GET /settings", s.page("setup.html"))
	mux.HandleFunc("GET /snapshots", s.page("snapshots.html"))
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
	mux.HandleFunc("POST /api/archive/open", s.openArchive)
	mux.HandleFunc("GET /api/backups/{place}/snapshots", s.snapshots)
	mux.HandleFunc("POST /api/backups/{place}/snapshots/{id}/restore", s.restoreCopy)
	mux.HandleFunc("POST /api/backups/{place}/snapshots/{id}/open", s.openCopy)
	// The guard comes first: requests it refuses (any website can send them)
	// do not reach the log.
	return s.guard(s.logged(mux))
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

// guard rejects what a page from another site could make the browser do: a
// foreign Host (DNS rebinding), any API call without the token only our pages
// carry (a custom header, so the browser asks first and gets no CORS), and
// loading our pages as a subresource.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || port != s.port || !localHost(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Tgbup-Token")), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") && !s.signedIn(w, r) {
			http.Error(w, "Open the address tgbup serve printed (it carries a key), or run tgbup serve --open.", http.StatusForbidden)
			return
		}
		// Other sites may link here but not load anything.
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
		if err := s.pages.ExecuteTemplate(w, name, map[string]string{"Token": s.token}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.backend.Status(r.Context())
	reply(w, st, err)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	err := s.backend.StartRun(r.URL.Query().Get("takeout") == "1")
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
	ts, err := s.backend.Topics(r.Context(), id, r.URL.Query().Get("counts") == "1")
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
	p, err := s.backend.Preview(r.Context(), sel, r.URL.Query().Get("counts") == "1")
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

func (s *Server) openArchive(w http.ResponseWriter, _ *http.Request) {
	reply(w, map[string]bool{"ok": true}, s.backend.OpenArchive())
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	place, err := strconv.Atoi(r.PathValue("place"))
	if err != nil {
		http.Error(w, "bad place", http.StatusBadRequest)
		return
	}
	snaps, err := s.backend.Snapshots(r.Context(), place)
	reply(w, snaps, err)
}

func (s *Server) restoreCopy(w http.ResponseWriter, r *http.Request) {
	place, err := strconv.Atoi(r.PathValue("place"))
	if err != nil {
		http.Error(w, "bad place", http.StatusBadRequest)
		return
	}
	// Not tied to the page: closing the tab must not stop a restore half-way.
	snap, err := s.backend.RestoreCopy(context.WithoutCancel(r.Context()), place, r.PathValue("id"))
	reply(w, snap, err)
}

func (s *Server) openCopy(w http.ResponseWriter, r *http.Request) {
	place, err := strconv.Atoi(r.PathValue("place"))
	if err != nil {
		http.Error(w, "bad place", http.StatusBadRequest)
		return
	}
	reply(w, map[string]bool{"ok": true}, s.backend.OpenCopy(place, r.PathValue("id")))
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

// Serve runs the interface on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	// Requests share ctx, so a shutdown also ends the ones waiting on Telegram.
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
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
