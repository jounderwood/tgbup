//go:build e2e

// Package e2e runs the real tgbup binary against Telegram's test servers on data
// prepared by `make e2e-seed`; seeding and cleanup are separate steps so the
// forum can be inspected in a Telegram client between them.
package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"

	"github.com/joeunderwood/tgbup/e2e/testenv"
)

var (
	binPath string
	fx      testenv.Fixture
	// testSecrets and testState isolate the binary from the real secrets and
	// state: a copy of the test-server session and of the API keys.
	testSecrets, testState string
)

// The suite must never reach production Telegram: the Telegram client in
// testenv and every binary run here are pinned to the test servers, whatever
// the caller's environment says.
func TestMain(m *testing.M) {
	if err := testenv.PinTestServers(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var err error
	if fx, err = testenv.LoadFixture(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "tgbup-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "tgbup")
	if out, err := exec.Command("go", "build", "-o", binPath, "../cmd/tgbup").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build tgbup: %v\n%s", err, out)
		os.Exit(1)
	}
	if testSecrets, testState, err = testenv.IsolatedState(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// testServerEnv drops any inherited TGBUP_TEST_DC/TGBUP_CONFIG and pins the binary to the test servers.
func testServerEnv(cfgPath string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TGBUP_TEST_DC=") || strings.HasPrefix(kv, "TGBUP_CONFIG=") ||
			strings.HasPrefix(kv, "TGBUP_SECRETS=") || strings.HasPrefix(kv, "TGBUP_STATE_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TGBUP_TEST_DC=1", "TGBUP_CONFIG="+cfgPath, "TGBUP_SECRETS="+testSecrets, "TGBUP_STATE_DIR="+testState)
}

func TestSyncEnvironmentIsPinnedToTestServers(t *testing.T) {
	t.Setenv("TGBUP_TEST_DC", "0")
	n := 0
	for _, kv := range testServerEnv("/c.yaml") {
		if strings.HasPrefix(kv, "TGBUP_TEST_DC=") {
			n++
			if kv != "TGBUP_TEST_DC=1" {
				t.Fatalf("binary would run with %s", kv)
			}
		}
	}
	if n != 1 {
		t.Fatalf("TGBUP_TEST_DC appears %d times", n)
	}
}

// newArchive returns a config selecting the fixture forum through its folder,
// with a fresh archive directory per test.
func newArchive(t *testing.T) (cfgPath, archive string) {
	t.Helper()
	dir := t.TempDir()
	archive = filepath.Join(dir, "archive")
	cfgPath = filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf("archive_dir: %s\ntargets:\n  folders: [%q]\n", archive, fx.Folder)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, archive
}

func runSync(t *testing.T, cfgPath string, args ...string) string {
	t.Helper()
	return runTgbup(t, cfgPath, append([]string{"sync"}, args...)...)
}

func runTgbup(t *testing.T, cfgPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = testServerEnv(cfgPath)
	out, err := cmd.CombinedOutput()
	t.Logf("tgbup %s:\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("tgbup %s failed: %v", args[0], err)
	}
	return string(out)
}

func openArchive(t *testing.T, archive string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(archive, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func scalar[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("%s %v: %v", query, args, err)
	}
	return v
}

// mediaFile returns the downloaded file of a message's media, failing unless it is done.
func mediaFile(t *testing.T, db *sql.DB, archive string, chatID int64, msgID int) []byte {
	t.Helper()
	var status, path sql.NullString
	err := db.QueryRow(`SELECT m.status, m.local_path FROM media m
JOIN message_media mm ON mm.media_key = m.key WHERE mm.chat_id = ? AND mm.message_id = ?`, chatID, msgID).Scan(&status, &path)
	if err != nil || status.String != "done" {
		t.Errorf("media of %d/%d: status %q, err %v", chatID, msgID, status.String, err)
		return nil
	}
	b, err := os.ReadFile(filepath.Join(archive, path.String))
	if err != nil {
		t.Errorf("media of %d/%d: %v", chatID, msgID, err)
	}
	return b
}

func sameSHA256(b []byte, want string) bool {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == want
}

func checkMedia(t *testing.T, db *sql.DB, archive string) {
	t.Helper()
	if photo := mediaFile(t, db, archive, fx.ChatID, fx.PhotoMsg); len(photo) == 0 {
		t.Errorf("forum photo is empty")
	}
	if doc := mediaFile(t, db, archive, fx.ChatID, fx.DocMsg); !sameSHA256(doc, fx.DocSHA256) {
		t.Errorf("forum document differs from the uploaded file (%d vs %d bytes)", len(doc), fx.DocSize)
	}
	if doc := mediaFile(t, db, archive, fx.SelfID, fx.SavedDocMsg); !sameSHA256(doc, fx.SavedDocSHA256) {
		t.Errorf("Saved Messages document differs from the uploaded file")
	}
}

func checkPrivateAndGroup(t *testing.T, db *sql.DB) {
	t.Helper()
	if typ := scalar[string](t, db, `SELECT type FROM chats WHERE id = ?`, fx.SelfID); typ != "private" {
		t.Errorf("Saved Messages type = %s, want private", typ)
	}
	for _, id := range fx.SavedMsgs {
		if n := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ? AND id = ?`, fx.SelfID, id); n != 1 {
			t.Errorf("Saved Messages message %d not archived", id)
		}
	}
	if typ := scalar[string](t, db, `SELECT type FROM chats WHERE id = ?`, fx.GroupChatID()); typ != "group" {
		t.Errorf("basic group type = %s, want group", typ)
	}
	text := scalar[string](t, db, `SELECT text FROM messages WHERE chat_id = ? AND id = ?`, fx.GroupChatID(), fx.GroupMsg)
	if text != "семейный чат: в субботу у бабушки" {
		t.Errorf("group message = %q", text)
	}
}

// checkMediaNames looks for the seeded documents under their own names in
// media/: one in the forum, one in Saved Messages. The test account sent both,
// so neither is quarantined.
func checkMediaNames(t *testing.T, archive string) {
	t.Helper()
	pattern := filepath.Join(archive, "media", "*", "file", "*_отчёт 2026*.pdf")
	m, _ := filepath.Glob(pattern)
	if len(m) != 2 {
		t.Errorf("%d matches for %s: %q", len(m), pattern, m)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	for _, p := range m {
		if _, err := unix.Getxattr(p, "com.apple.quarantine", make([]byte, 128)); err == nil {
			t.Errorf("own file %s is quarantined", p)
		}
	}
}

// checkMeta finds the seeded location, place, contact and poll in meta_json.
func checkMeta(t *testing.T, db *sql.DB) {
	t.Helper()
	for id, want := range map[int]string{
		fx.GeoMsg: `"lat":48.8584`, fx.VenueMsg: `"title":"Cafe de Flore"`,
		fx.ContactMsg: `"phone":"+99966612345"`, fx.PollMsg: `"question":"Dacha on Saturday?"`,
	} {
		got := scalar[sql.NullString](t, db, `SELECT meta_json FROM messages WHERE chat_id = ? AND id = ?`, fx.ChatID, id)
		if !strings.Contains(got.String, want) {
			t.Errorf("message %d meta = %q, want %s", id, got.String, want)
		}
	}
}

// checkRendered finds the seeded document on a forum page and follows its link.
func checkRendered(t *testing.T, archive string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(archive, "html", "index.html")); err != nil {
		t.Errorf("html/index.html: %v", err)
	}
	pages, _ := filepath.Glob(filepath.Join(archive, "html", fx.Title, "*", "*.html"))
	linked := false
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		i := strings.Index(string(b), "📄 отчёт 2026.pdf")
		if i < 0 {
			continue
		}
		head := string(b[:i])
		href := head[strings.LastIndex(head, `href="`)+len(`href="`):]
		href = href[:strings.Index(href, `"`)]
		file, err := url.PathUnescape(href)
		if err != nil {
			t.Fatalf("link %s: %v", href, err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), filepath.FromSlash(file))); err != nil {
			t.Errorf("document link %s on %s is broken: %v", href, p, err)
		}
		linked = true
	}
	if !linked {
		t.Errorf("no forum page links the seeded document (pages: %v)", pages)
	}
	var all strings.Builder
	for _, p := range pages {
		b, _ := os.ReadFile(p)
		all.Write(b)
	}
	for _, want := range []string{"📍 Cafe de Flore, 172 Boulevard Saint-Germain", "google.com/maps/search/?api=1&amp;query=48.85",
		"👤 Test Contact", "📊 Dacha on Saturday?"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("forum pages lack %q", want)
		}
	}
	if md, _ := filepath.Glob(filepath.Join(archive, "md", fx.Title, "*", "*.md")); len(md) == 0 {
		t.Error("no Markdown pages for the forum")
	}
}

func checkExport(t *testing.T, cfgPath string) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "forum.zip")
	runTgbup(t, cfgPath, "export", fx.Title, "-o", zipPath)
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var doc, page bool
	for _, f := range r.File {
		doc = doc || (strings.HasPrefix(f.Name, "forum/media/") && strings.HasSuffix(f.Name, "_отчёт 2026.pdf"))
		page = page || strings.HasPrefix(f.Name, "forum/html/"+fx.Title+"/")
	}
	if !doc || !page {
		t.Errorf("export zip: document %v, forum pages %v", doc, page)
	}
}

func TestSyncArchivesForum(t *testing.T) {
	cfgPath, archive := newArchive(t)
	runSync(t, cfgPath)
	db := openArchive(t, archive)

	if typ := scalar[string](t, db, `SELECT type FROM chats WHERE id = ?`, fx.ChatID); typ != "forum" {
		t.Errorf("chat type = %s, want forum", typ)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM topics WHERE chat_id = ? AND topic_id = ? AND title = 'Рецепты'`, fx.ChatID, fx.TopicID); n != 1 {
		t.Errorf("topic not archived")
	}
	msg := func(col string, id int) sql.NullString {
		return scalar[sql.NullString](t, db, `SELECT `+col+` FROM messages WHERE chat_id = ? AND id = ?`, fx.ChatID, id)
	}
	if e := msg("entities_json", fx.LinkMsg); !bytes.Contains([]byte(e.String), []byte(`"url":"https://example.com/doc"`)) {
		t.Errorf("hidden link lost: %q", e.String)
	}
	if r := msg("reply_to_id", fx.ReplyMsg); r.String != fmt.Sprint(fx.LinkMsg) {
		t.Errorf("reply_to_id = %q, want %d", r.String, fx.LinkMsg)
	}
	if tp := msg("topic_id", fx.TopicMsg); tp.String != fmt.Sprint(fx.TopicID) {
		t.Errorf("topic_id = %q, want %d", tp.String, fx.TopicID)
	}
	if fw := msg("fwd_from_json", fx.FwdMsg); !fw.Valid {
		t.Errorf("forward header lost")
	}
	if p := msg("pinned", fx.ReplyMsg); p.String != "1" {
		t.Errorf("pinned = %q", p.String)
	}
	for _, action := range []string{"messageActionTopicCreate", "messageActionPinMessage"} {
		n := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ? AND service_action_json LIKE ?`, fx.ChatID, `%`+action+`%`)
		if n == 0 {
			t.Errorf("service message %s not archived", action)
		}
	}
	checkPrivateAndGroup(t, db)
	checkMedia(t, db, archive)
	checkMediaNames(t, archive)
	checkMeta(t, db)
	checkRendered(t, archive)
	checkExport(t, cfgPath)

	before := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ?`, fx.ChatID)
	if err := testenv.WithTelegram(func(ctx context.Context, raw *tg.Client) error {
		for i := range 3 {
			if _, err := testenv.SendText(ctx, raw, fx.Peer(), fmt.Sprintf("новое %d", i), nil, 0); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runSync(t, cfgPath)
	if after := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ?`, fx.ChatID); after != before+3 {
		t.Errorf("incremental sync: %d → %d messages, want +3", before, after)
	}
}

func TestDryRunListsChatsWithoutArchiving(t *testing.T) {
	cfgPath, archive := newArchive(t)
	out := runSync(t, cfgPath, "--dry-run")
	if !strings.Contains(out, "would archive 3 chats") || !strings.Contains(out, "══ Total: 3 chats") {
		t.Errorf("dry run output:\n%s", out)
	}
	for _, id := range []int64{fx.ChatID, fx.SelfID, fx.GroupChatID()} {
		if !strings.Contains(out, fmt.Sprint(id)) {
			t.Errorf("chat %d missing from dry run", id)
		}
	}
	if _, err := os.Stat(filepath.Join(archive, "archive.sqlite")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dry run touched the archive: %v", err)
	}
}

// An excluded topic stays out of the archive and comes back, with its earlier
// messages, once the exclusion is dropped.
func TestTopicExclusion(t *testing.T) {
	cfgPath, archive := newArchive(t)
	writeConfig := func(exclude bool) {
		t.Helper()
		cfg := fmt.Sprintf("archive_dir: %s\ntargets:\n  folders: [%q]\n", archive, fx.Folder)
		if exclude {
			cfg += fmt.Sprintf("  exclude_topics:\n    %d: [%d]\n", fx.ChatID, fx.TopicID)
		}
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeConfig(true)
	out := runSync(t, cfgPath, "--dry-run")
	for _, want := range []string{"excluded:", fmt.Sprintf("Рецепты #%d", fx.TopicID), "General #1", "photos 1", "files"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	runSync(t, cfgPath)
	db := openArchive(t, archive)
	has := func(id int) bool {
		return scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ? AND id = ?`, fx.ChatID, id) == 1
	}
	if has(fx.TopicMsg) || !has(fx.LinkMsg) {
		t.Fatalf("with the topic excluded: topic message kept %v, General message kept %v", has(fx.TopicMsg), has(fx.LinkMsg))
	}

	writeConfig(false)
	runSync(t, cfgPath)
	if !has(fx.TopicMsg) {
		t.Fatal("re-included topic's earlier message was not archived")
	}
}

// Each takeout session makes Telegram ask the account to confirm a data export,
// so this runs only on request: make e2e-takeout.
func TestTakeoutSync(t *testing.T) {
	if os.Getenv("TGBUP_E2E_TAKEOUT") != "1" {
		t.Skip("takeout check is opt-in: make e2e-takeout")
	}
	cfgPath, archive := newArchive(t)
	runSync(t, cfgPath, "--takeout")

	db := openArchive(t, archive)
	if n := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ? AND id IN (?, ?, ?, ?)`,
		fx.ChatID, fx.LinkMsg, fx.ReplyMsg, fx.TopicMsg, fx.FwdMsg); n != 4 {
		t.Errorf("takeout archived %d of 4 seeded text messages", n)
	}
	checkPrivateAndGroup(t, db)
	checkMedia(t, db, archive)
}
