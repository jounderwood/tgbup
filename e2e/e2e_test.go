//go:build e2e

// Package e2e runs the real tgbup binary against Telegram's test servers on data
// prepared by `make e2e-seed`; seeding and cleanup are separate steps so the
// forum can be inspected in a Telegram client between them.
package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	_ "modernc.org/sqlite"

	"gitlab.com/joeunderwood/tgbup/e2e/testenv"
)

var (
	binPath string
	fx      testenv.Fixture
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
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// testServerEnv drops any inherited TGBUP_TEST_DC/TGBUP_CONFIG and pins the binary to the test servers.
func testServerEnv(cfgPath string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TGBUP_TEST_DC=") || strings.HasPrefix(kv, "TGBUP_CONFIG=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TGBUP_TEST_DC=1", "TGBUP_CONFIG="+cfgPath)
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

func runSync(t *testing.T, cfgPath string, args ...string) {
	t.Helper()
	cmd := exec.Command(binPath, append([]string{"sync"}, args...)...)
	cmd.Env = testServerEnv(cfgPath)
	out, err := cmd.CombinedOutput()
	t.Logf("tgbup sync %s:\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("tgbup sync failed: %v", err)
	}
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

func checkMedia(t *testing.T, db *sql.DB, archive string) {
	t.Helper()
	if n := scalar[int](t, db, `SELECT count(*) FROM media WHERE status = 'done'`); n != 2 {
		t.Errorf("media done = %d, want 2 (photo + document)", n)
	}
	docPath := scalar[string](t, db, `SELECT local_path FROM media WHERE kind = 'document'`)
	got, err := os.ReadFile(filepath.Join(archive, docPath))
	sum := sha256.Sum256(got)
	if err != nil || hex.EncodeToString(sum[:]) != fx.DocSHA256 {
		t.Errorf("document differs from the uploaded file (err=%v, %d vs %d bytes)", err, len(got), fx.DocSize)
	}
	photoPath := scalar[string](t, db, `SELECT local_path FROM media WHERE kind = 'photo'`)
	if st, err := os.Stat(filepath.Join(archive, photoPath)); err != nil || st.Size() == 0 {
		t.Errorf("photo file missing: %v", err)
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
	checkMedia(t, db, archive)

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

func TestTakeoutSync(t *testing.T) {
	cfgPath, archive := newArchive(t)
	runSync(t, cfgPath, "--takeout")

	db := openArchive(t, archive)
	if n := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ? AND id IN (?, ?, ?, ?)`,
		fx.ChatID, fx.LinkMsg, fx.ReplyMsg, fx.TopicMsg, fx.FwdMsg); n != 4 {
		t.Errorf("takeout archived %d of 4 seeded text messages", n)
	}
	checkMedia(t, db, archive)
}
