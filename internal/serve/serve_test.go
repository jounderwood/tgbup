package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func (f *fakeBackend) OpenArchive() error { f.opened++; return nil }

func (f *fakeBackend) Snapshots(_ context.Context, place int) ([]Snapshot, error) {
	return []Snapshot{{ID: "abcd1234", Host: "mac"}}, nil
}

type fakeBackend struct {
	opened  int
	actions []string
	busy    bool
	runs    int
	saved   *Selection
}

func (f *fakeBackend) Status(context.Context) (Status, error) {
	return Status{Host: "mac", Archive: Archive{Messages: 42}}, nil
}

func (f *fakeBackend) StartRun() error {
	if f.busy {
		return ErrBusy
	}
	f.runs++
	f.busy = true
	return nil
}

func (f *fakeBackend) RunLog() (string, error) { return "line 1\nline 2", nil }

func (f *fakeBackend) Chats(context.Context) (ChatList, error) {
	return ChatList{Groups: []Group{{Title: "All chats", IDs: []int64{-1002263466511}}},
		Chats: []Chat{{ID: -1002263466511, Title: "Family", Type: "forum", Selected: true, ExcludedTopics: []int{5}}}}, nil
}

func (f *fakeBackend) Counts(_ context.Context, ids []int64) (map[int64]Count, error) {
	out := map[int64]Count{}
	for _, id := range ids {
		out[id] = Count{Messages: 10, Topics: 2}
	}
	return out, nil
}

func (f *fakeBackend) Topics(context.Context, int64) ([]Topic, error) {
	return []Topic{{ID: 1, Title: "General", Messages: 3}}, nil
}

func (f *fakeBackend) Preview(_ context.Context, sel Selection) (Preview, error) {
	return Preview{Report: "would archive 1 chat", Config: "targets: …"}, nil
}

func (f *fakeBackend) SetupState(context.Context) (any, error) {
	return map[string]any{"complete": false}, nil
}

func (f *fakeBackend) SetupAction(_ context.Context, name string, body []byte) (any, error) {
	if name == "keys" && !strings.Contains(string(body), "api_id") {
		return nil, fmt.Errorf("%w: api_id is a number", ErrBadRequest)
	}
	f.actions = append(f.actions, name)
	return nil, nil
}

func (f *fakeBackend) Save(_ context.Context, sel Selection) error {
	if len(sel.Chats) > 0 && sel.Chats[0] == 404 {
		return fmt.Errorf("%w: chat 404 is not in your chat list", ErrBadRequest)
	}
	f.saved = &sel
	return nil
}

func testServer(t *testing.T) (*Server, *fakeBackend, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"archive.sqlite":        "secret db",
		"files/a.txt":           "file view",
		"html/index.html":       "<h1>archive</h1>",
		"html/Family/p1.html":   "<p>page</p>",
		"media/photo/1.jpg":     "jpeg",
		"media/document/1.html": "<script>fetch('/')</script>",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeBackend{}
	s, err := New(f, "8640")
	if err != nil {
		t.Fatal(err)
	}
	return s, f, dir
}

func do(t *testing.T, s *Server, method, path, host, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if token != "" {
		r.Header.Set("X-Tgbup-Token", token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

const host = "localhost:8640"

func token(t *testing.T, s *Server) string {
	t.Helper()
	page := do(t, s, "GET", "/", host, "", "").Body.String()
	m := regexp.MustCompile(`name="tgbup-token" content="([0-9a-f]{32})"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no token in the page:\n%s", page)
	}
	return m[1]
}

func TestForeignHostIsRefused(t *testing.T) {
	s, _, _ := testServer(t)
	for _, h := range []string{"evil.example:8640", "localhost:9999", "localhost", "192.168.1.5:8640"} {
		if w := do(t, s, "GET", "/api/status", h, "", ""); w.Code != http.StatusForbidden {
			t.Errorf("host %s: %d, want 403", h, w.Code)
		}
	}
	tok := token(t, s)
	for _, h := range []string{"localhost:8640", "127.0.0.1:8640", "[::1]:8640"} {
		if w := do(t, s, "GET", "/api/status", h, tok, ""); w.Code != http.StatusOK {
			t.Errorf("host %s: %d, want 200", h, w.Code)
		}
	}
}

func TestChangesNeedTheToken(t *testing.T) {
	s, f, _ := testServer(t)
	for _, tok := range []string{"", "wrong"} {
		if w := do(t, s, "POST", "/api/run", host, tok, ""); w.Code != http.StatusForbidden {
			t.Errorf("token %q: %d, want 403", tok, w.Code)
		}
	}
	if f.runs != 0 {
		t.Fatal("a run started without the token")
	}
	tok := token(t, s)
	for _, p := range []string{"/api/status", "/api/chats", "/api/chats/1/topics", "/api/run/log"} {
		if w := do(t, s, "GET", p, host, "", ""); w.Code != http.StatusForbidden {
			t.Errorf("GET %s without the token: %d, want 403 (an <img> could fire it)", p, w.Code)
		}
	}
	if w := do(t, s, "POST", "/api/run", host, tok, ""); w.Code != http.StatusAccepted || f.runs != 1 {
		t.Fatalf("with token: %d, runs %d", w.Code, f.runs)
	}
	if w := do(t, s, "POST", "/api/run", host, tok, ""); w.Code != http.StatusConflict {
		t.Fatalf("second run while busy: %d, want 409", w.Code)
	}
}

func TestAPI(t *testing.T) {
	s, f, _ := testServer(t)
	tok := token(t, s)

	var st Status
	if err := json.NewDecoder(do(t, s, "GET", "/api/status", host, tok, "").Body).Decode(&st); err != nil || st.Archive.Messages != 42 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if got := do(t, s, "GET", "/api/run/log", host, tok, "").Body.String(); got != "line 1\nline 2" {
		t.Errorf("log = %q", got)
	}
	var list ChatList
	if err := json.NewDecoder(do(t, s, "GET", "/api/chats", host, tok, "").Body).Decode(&list); err != nil || list.Chats[0].ID != -1002263466511 {
		t.Fatalf("chats = %+v, %v", list, err)
	}
	counts := do(t, s, "POST", "/api/chats/counts", host, tok, `{"ids":[-1002263466511]}`).Body.String()
	if !strings.Contains(counts, `"-1002263466511":{"messages":10,"topics":2}`) {
		t.Errorf("counts = %s", counts)
	}
	if w := do(t, s, "GET", "/api/chats/x/topics", host, tok, ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad topic id: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/chats", host, tok, `{"chats":[]}`); w.Code != http.StatusBadRequest || f.saved != nil {
		t.Errorf("empty selection: %d, saved %v", w.Code, f.saved)
	}
	w := do(t, s, "POST", "/api/chats", host, tok, `{"chats":[-1002263466511,10],"exclude_topics":{"-1002263466511":[5,12]}}`)
	if w.Code != http.StatusNoContent || f.saved == nil {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := f.saved.ExcludeTopics[-1002263466511]; len(got) != 2 || got[1] != 12 || f.saved.Chats[1] != 10 {
		t.Errorf("saved = %+v", f.saved)
	}
	if w := do(t, s, "POST", "/api/chats", host, tok, `{"chats":[404]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown chat: %d, want 400", w.Code)
	}
}

func TestStaticAndPages(t *testing.T) {
	s, _, _ := testServer(t)
	for _, p := range []string{"/", "/setup", "/static/app.css", "/static/common.js", "/static/overview.js", "/static/chats.js"} {
		w := do(t, s, "GET", p, host, "", "")
		body, _ := io.ReadAll(w.Body)
		if w.Code != 200 || len(body) == 0 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if csp := do(t, s, "GET", "/", host, "", "").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	// The chat list is a step of Setup now.
	if w := do(t, s, "GET", "/chats", host, "", ""); w.Code != http.StatusFound || w.Header().Get("Location") != "/setup#chats" {
		t.Errorf("/chats: %d %s", w.Code, w.Header().Get("Location"))
	}
	// The archive is not served: its pages are opened from the disk.
	for _, p := range []string{"/html/index.html", "/media/photo/1.jpg", "/archive.sqlite"} {
		if w := do(t, s, "GET", p, host, "", ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestArchiveAndSnapshots(t *testing.T) {
	s, f, _ := testServer(t)
	tok := token(t, s)
	if w := do(t, s, "POST", "/api/archive/open", host, tok, ""); w.Code != 200 || f.opened != 1 {
		t.Errorf("open: %d, opened %d", w.Code, f.opened)
	}
	w := do(t, s, "GET", "/api/backups/0/snapshots", host, tok, "")
	var snaps []Snapshot
	if err := json.NewDecoder(w.Body).Decode(&snaps); err != nil || len(snaps) != 1 || snaps[0].ID != "abcd1234" {
		t.Errorf("snapshots: %d %+v %v", w.Code, snaps, err)
	}
	if w := do(t, s, "GET", "/api/backups/x/snapshots", host, tok, ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad place: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/archive/open", host, "", ""); w.Code != http.StatusForbidden {
		t.Errorf("open without the token: %d", w.Code)
	}
}

func TestCheckListen(t *testing.T) {
	for addr, ok := range map[string]bool{"127.0.0.1:8640": true, "localhost:1": true, "[::1]:8640": true,
		"0.0.0.0:8640": false, ":8640": false, "192.168.1.2:80": false, "nonsense": false, "127.0.0.2:8640": false} {
		if err := CheckListen(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

func TestOtherOriginsCannotLoadTheInterface(t *testing.T) {
	s, _, _ := testServer(t)
	r := httptest.NewRequest("GET", "/static/app.css", nil)
	r.Host = host
	r.Header.Set("Sec-Fetch-Site", "same-site") // e.g. a page on the archive origin
	r.Header.Set("Sec-Fetch-Mode", "no-cors")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site subresource: %d", w.Code)
	}
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.URL.Path = "/"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("navigating from another site (a link): %d", w.Code)
	}
}

func TestSetupRoutes(t *testing.T) {
	s, f, _ := testServer(t)
	tok := token(t, s)
	if w := do(t, s, "GET", "/setup", host, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "setup.js") {
		t.Fatalf("setup page: %d", w.Code)
	}
	if w := do(t, s, "GET", "/api/setup", host, tok, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "complete") {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	if w := do(t, s, "POST", "/api/setup/keys", host, "", `{"api_id":"1"}`); w.Code != 403 {
		t.Fatalf("setup action without the token: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/setup/keys", host, tok, `{}`); w.Code != 400 || !strings.Contains(w.Body.String(), "api_id is a number") {
		t.Fatalf("bad input: %d %s", w.Code, w.Body)
	}
	if w := do(t, s, "POST", "/api/setup/login/code", host, tok, `{"phone":"+1"}`); w.Code != 200 || f.actions[len(f.actions)-1] != "login/code" {
		t.Fatalf("nested action name: %d %v", w.Code, f.actions)
	}
}

// The log names each action and a failure's message, never what was typed.
func TestLogShowsActionsWithoutTheirInput(t *testing.T) {
	s, _, _ := testServer(t)
	var buf bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&buf, nil))
	do(t, s, "POST", "/api/setup/keys", host, s.token, `{"api_hash":"SECRET-VALUE"}`)
	do(t, s, "POST", "/api/setup/password/new", host, s.token, `{"password":"SECRET-VALUE"}`)
	do(t, s, "GET", "/favicon.ico", host, "", "")
	out := buf.String()
	for _, want := range []string{"/api/setup/keys failed", "api_id is a number", "/api/setup/password/new done"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET-VALUE") || strings.Contains(out, "favicon") {
		t.Errorf("log shows input or noise:\n%s", out)
	}
}
