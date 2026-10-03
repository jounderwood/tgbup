package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeBackend struct {
	busy  bool
	runs  int
	saved *Selection
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
	s, err := New(f, func() string { return dir }, "8640", "8641")
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
	if strings.HasSuffix(host, ":8641") {
		s.ArchiveHandler().ServeHTTP(w, r)
	} else {
		s.Handler().ServeHTTP(w, r)
	}
	return w
}

const archiveHost = "localhost:8641"

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

func TestArchiveServesOnlyTheSiteAndMedia(t *testing.T) {
	s, _, _ := testServer(t)
	for path, want := range map[string]int{
		"/html/":                      200,
		"/html/index.html":            200,
		"/html/Family/p1.html":        200,
		"/media/photo/1.jpg":          200,
		"/archive.sqlite":             404,
		"/files/a.txt":                404,
		"/html/../archive.sqlite":     404,
		"/html/%2e%2e/archive.sqlite": 404,
		"/media/../files/a.txt":       404,
		"/html/missing.html":          404,
	} {
		w := do(t, s, "GET", path, archiveHost, "", "")
		// The mux answers paths with .. by redirecting to the cleaned path, which is then a 404.
		if w.Code != want && (want == 200 || w.Code == 200) {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "secret db") || strings.Contains(w.Body.String(), "file view") {
			t.Errorf("%s leaked a file outside html/ and media/", path)
		}
	}
	// The archive is not on the interface's origin, and the API is not on the archive's.
	if w := do(t, s, "GET", "/html/", host, "", ""); w.Code != 404 {
		t.Errorf("archive on the interface origin: %d", w.Code)
	}
	if w := do(t, s, "GET", "/api/status", archiveHost, "", ""); w.Code == 200 {
		t.Errorf("API on the archive origin: %d", w.Code)
	}
	if w := do(t, s, "GET", "/", archiveHost, "", ""); w.Code != http.StatusFound {
		t.Errorf("archive root: %d", w.Code)
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
	for _, p := range []string{"/", "/chats", "/static/app.css", "/static/common.js", "/static/overview.js", "/static/chats.js"} {
		w := do(t, s, "GET", p, host, "", "")
		body, _ := io.ReadAll(w.Body)
		if w.Code != 200 || len(body) == 0 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if csp := do(t, s, "GET", "/", host, "", "").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
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

// Files come from other people: one opened as a page must not run scripts,
// and anything but pictures, video and sound is downloaded.
func TestArchiveIsSandboxed(t *testing.T) {
	s, _, _ := testServer(t)
	w := do(t, s, "GET", "/media/document/1.html", archiveHost, "", "")
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox;") {
		t.Errorf("media CSP = %q", csp)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "attachment" {
		t.Errorf("html from a chat is shown inline: %q", cd)
	}
	if cd := do(t, s, "GET", "/media/photo/1.jpg", archiveHost, "", "").Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("photo: %q, want inline", cd)
	}
	page := do(t, s, "GET", "/", host, "", "").Body.String()
	if !strings.Contains(page, `href="http://localhost:8641/html/"`) {
		t.Errorf("the Archive link does not point at the archive origin")
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

func TestSymlinkOutOfTheArchiveIsRefused(t *testing.T) {
	s, _, dir := testServer(t)
	if err := os.Symlink(filepath.Join(dir, "archive.sqlite"), filepath.Join(dir, "html", "db.html")); err != nil {
		t.Skip(err)
	}
	if w := do(t, s, "GET", "/html/db.html", archiveHost, "", ""); w.Code == http.StatusOK {
		t.Error("followed a symlink out of html/")
	}
}
