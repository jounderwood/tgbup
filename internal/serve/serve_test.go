package serve

import (
	"context"
	"encoding/json"
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
	f.saved = &sel
	return nil
}

func testServer(t *testing.T) (*Server, *fakeBackend, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"archive.sqlite":      "secret db",
		"files/a.txt":         "file view",
		"html/index.html":     "<h1>archive</h1>",
		"html/Family/p1.html": "<p>page</p>",
		"media/photo/1":       "jpeg",
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
	s, err := New(f, dir, "8640")
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

func TestForeignHostIsRefused(t *testing.T) {
	s, _, _ := testServer(t)
	for _, h := range []string{"evil.example:8640", "localhost:9999", "localhost", "192.168.1.5:8640"} {
		if w := do(t, s, "GET", "/api/status", h, "", ""); w.Code != http.StatusForbidden {
			t.Errorf("host %s: %d, want 403", h, w.Code)
		}
	}
	for _, h := range []string{"localhost:8640", "127.0.0.1:8640", "[::1]:8640"} {
		if w := do(t, s, "GET", "/api/status", h, "", ""); w.Code != http.StatusOK {
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
	page := do(t, s, "GET", "/", host, "", "").Body.String()
	tok := regexp.MustCompile(`name="tgbup-token" content="([0-9a-f]{32})"`).FindStringSubmatch(page)
	if tok == nil {
		t.Fatalf("no token in the page:\n%s", page)
	}
	if w := do(t, s, "POST", "/api/run", host, tok[1], ""); w.Code != http.StatusAccepted || f.runs != 1 {
		t.Fatalf("with token: %d, runs %d", w.Code, f.runs)
	}
	if w := do(t, s, "POST", "/api/run", host, tok[1], ""); w.Code != http.StatusConflict {
		t.Fatalf("second run while busy: %d, want 409", w.Code)
	}
}

func TestArchiveServesOnlyTheSiteAndMedia(t *testing.T) {
	s, _, _ := testServer(t)
	for path, want := range map[string]int{
		"/archive/html/":                      200,
		"/archive/html/index.html":            200,
		"/archive/html/Family/p1.html":        200,
		"/archive/media/photo/1":              200,
		"/archive/archive.sqlite":             404,
		"/archive/files/a.txt":                404,
		"/archive/html/../archive.sqlite":     404,
		"/archive/html/%2e%2e/archive.sqlite": 404,
		"/archive/media/../files/a.txt":       404,
		"/archive/html/missing.html":          404,
	} {
		w := do(t, s, "GET", path, host, "", "")
		// The mux answers paths with .. by redirecting to the cleaned path, which is then a 404.
		if w.Code != want && (want == 200 || w.Code == 200) {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "secret db") || strings.Contains(w.Body.String(), "file view") {
			t.Errorf("%s leaked a file outside html/ and media/", path)
		}
	}
}

func TestAPI(t *testing.T) {
	s, f, _ := testServer(t)
	page := do(t, s, "GET", "/chats", host, "", "").Body.String()
	tok := regexp.MustCompile(`content="([0-9a-f]{32})"`).FindStringSubmatch(page)[1]

	var st Status
	if err := json.NewDecoder(do(t, s, "GET", "/api/status", host, "", "").Body).Decode(&st); err != nil || st.Archive.Messages != 42 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if got := do(t, s, "GET", "/api/run/log", host, "", "").Body.String(); got != "line 1\nline 2" {
		t.Errorf("log = %q", got)
	}
	var list ChatList
	if err := json.NewDecoder(do(t, s, "GET", "/api/chats", host, "", "").Body).Decode(&list); err != nil || list.Chats[0].ID != -1002263466511 {
		t.Fatalf("chats = %+v, %v", list, err)
	}
	counts := do(t, s, "POST", "/api/chats/counts", host, tok, `{"ids":[-1002263466511]}`).Body.String()
	if !strings.Contains(counts, `"-1002263466511":{"messages":10,"topics":2}`) {
		t.Errorf("counts = %s", counts)
	}
	if w := do(t, s, "GET", "/api/chats/x/topics", host, "", ""); w.Code != http.StatusBadRequest {
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
		"0.0.0.0:8640": false, ":8640": false, "192.168.1.2:80": false, "nonsense": false} {
		if err := CheckListen(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}
