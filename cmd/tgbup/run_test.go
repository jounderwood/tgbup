package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/syncer"
)

type fakeBot struct {
	mu    sync.Mutex
	sent  []string
	quiet []bool // disable_notification of each message
	srv   *httptest.Server
}

func newFakeBot(t *testing.T) *fakeBot {
	b := &fakeBot{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		b.mu.Lock()
		b.sent = append(b.sent, m["text"].(string))
		q, _ := m["disable_notification"].(bool)
		b.quiet = append(b.quiet, q)
		b.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBot) bot() notify.Bot { return notify.Bot{Token: "1:a", ChatID: "1", BaseURL: b.srv.URL} }

type runEnv struct {
	parts   runParts
	db      *store.Store
	now     time.Time
	syncErr error
	backups int
	repoErr error
}

func newRunEnv(t *testing.T, bot notify.Bot) *runEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &runEnv{db: db, now: time.Date(2026, 10, 2, 0, 30, 0, 0, time.UTC)}
	e.parts = runParts{
		sync: func(context.Context) (syncSummary, error) {
			return syncSummary{sync: syncer.Report{Chats: 2, NewMessages: 10}}, e.syncErr
		},
		backup: func(ctx context.Context) (backup.Report, bool) {
			e.backups++
			if e.repoErr == nil {
				_ = backup.RecordSuccess(ctx, db, "icloud", e.now)
			}
			return backup.Report{Repos: []backup.RepoResult{{Repo: "icloud", Snapshot: "abc", Err: e.repoErr}}}, true
		},
		db: db, bot: bot, host: "mac", archiveDir: dir, repos: []string{"icloud"},
		summaryEvery: 7 * 24 * time.Hour, now: func() time.Time { return e.now }, loc: time.UTC,
		out: io.Discard, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e
}

func TestRunBacksUpEvenWhenSyncFails(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	e.syncErr = errors.New("telegram unreachable")
	if code := runOnce(context.Background(), e.parts); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if e.backups != 1 {
		t.Fatal("no backup after a failed sync")
	}
	runs, _ := e.db.RunsSince(context.Background(), time.Time{})
	if len(runs) != 1 || runs[0].OK || !strings.Contains(runs[0].Summary, "telegram unreachable") {
		t.Fatalf("runs = %+v", runs)
	}
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "telegram unreachable") {
		t.Fatalf("bot got %q", bot.sent)
	}
}

func TestRunReportsBackupErrors(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	e.repoErr = errors.New("restic backup icloud: disk full")
	if code := runOnce(context.Background(), e.parts); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "disk full") {
		t.Fatalf("bot got %q", bot.sent)
	}
}

func TestRunSendsSummaryOncePerInterval(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	ctx := context.Background()
	if code := runOnce(ctx, e.parts); code != 0 || len(bot.sent) != 0 {
		t.Fatalf("first run: code %d, sent %q (the first run only starts the interval)", code, bot.sent)
	}
	for range 3 {
		e.now = e.now.Add(6 * time.Hour)
		runOnce(ctx, e.parts)
	}
	if len(bot.sent) != 0 {
		t.Fatalf("summary before the interval: %q", bot.sent)
	}
	e.now = e.now.Add(7 * 24 * time.Hour)
	runOnce(ctx, e.parts)
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "📦") || !strings.Contains(bot.sent[0], "+50 messages") || !strings.Contains(bot.sent[0], "icloud: 2026-10-") {
		t.Fatalf("summary = %q", bot.sent)
	}
	e.now = e.now.Add(6 * time.Hour)
	runOnce(ctx, e.parts)
	if len(bot.sent) != 1 {
		t.Fatalf("a second summary right after the first: %q", bot.sent)
	}
}

func TestRunWithoutBotOrBackup(t *testing.T) {
	e := newRunEnv(t, notify.Bot{})
	var out bytes.Buffer
	e.parts.out = &out
	e.parts.backup = func(context.Context) (backup.Report, bool) { return backup.Report{}, false }
	if code := runOnce(context.Background(), e.parts); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"backup: off", "alerts: no bot set up"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// A run that cannot even start (a config broken by an edit) must still reach the bot.
func TestEarlyFailureIsNotified(t *testing.T) {
	bot := newFakeBot(t)
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	if code := failEarly(context.Background(), log, bot.bot(), "load the config", errors.New("yaml: line 12: did not find expected key")); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "load the config: yaml: line 12") {
		t.Fatalf("bot got %q", bot.sent)
	}
}

func TestNewlyInaccessibleChatIsReported(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	e.parts.sync = func(context.Context) (syncSummary, error) {
		return syncSummary{sync: syncer.Report{Chats: 1, NewlyInaccessible: []int64{-1001}}}, nil
	}
	if code := runOnce(context.Background(), e.parts); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "-1001 is no longer accessible") {
		t.Fatalf("sent = %q", bot.sent)
	}
}

func TestHalfConfiguredBotIsNamed(t *testing.T) {
	e := newRunEnv(t, notify.Bot{Token: "1:a"})
	var out bytes.Buffer
	e.parts.out = &out
	runOnce(context.Background(), e.parts)
	if !strings.Contains(out.String(), "must both be set") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestPartialSnapshotIsReported(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	e.parts.backup = func(context.Context) (backup.Report, bool) {
		return backup.Report{Repos: []backup.RepoResult{{Repo: "/b", Snapshot: "abcdef12", Warning: "open media/x: permission denied"}}}, true
	}
	if code := runOnce(context.Background(), e.parts); code != 1 {
		t.Fatalf("code = %d: a snapshot missing files is not a clean run", code)
	}
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "some files could not be read") {
		t.Fatalf("sent = %q", bot.sent)
	}
}

// With progress on, a run tells the bot quietly that it started and finished;
// an error still rings.
func TestRunTellsTheBotItsProgress(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	e.parts.progress, e.parts.trigger, e.parts.takeout = true, "page", true
	if code := runOnce(context.Background(), e.parts); code != 0 {
		t.Fatalf("code %d", code)
	}
	if len(bot.sent) != 2 || !strings.Contains(bot.sent[0], "▶ tgbup on mac: download through a Telegram data export started from the page") ||
		!strings.Contains(bot.sent[1], "✓ tgbup on mac: 10 new messages, 0 new files; snapshot icloud abc") ||
		!bot.quiet[0] || !bot.quiet[1] {
		t.Fatalf("sent %q quiet %v", bot.sent, bot.quiet)
	}
	bot.sent, bot.quiet = nil, nil
	e.syncErr = errors.New("telegram unreachable")
	runOnce(context.Background(), e.parts)
	if len(bot.sent) != 2 || !strings.Contains(bot.sent[1], "telegram unreachable") || bot.quiet[1] {
		t.Fatalf("an error: sent %q quiet %v", bot.sent, bot.quiet)
	}
}

func TestUnchangedRunSaysNoSnapshotWasNeeded(t *testing.T) {
	bot := newFakeBot(t)
	e := newRunEnv(t, bot.bot())
	e.parts.progress = true
	ctx := context.Background()
	if err := e.db.UpsertChat(ctx, store.Chat{ID: 10, Type: store.ChatPrivate, Title: "Mom"}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SaveBatch(ctx, store.Batch{ChatID: 10, Messages: []store.Message{{ChatID: 10, ID: 1, Date: 1, Raw: []byte{1}}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.db.NoteUpdate(ctx, 0, e.now); err != nil {
		t.Fatal(err)
	}
	e.parts.backup = func(context.Context) (backup.Report, bool) {
		return backup.Report{Repos: []backup.RepoResult{{Repo: "icloud", Snapshot: "abc", Skipped: true}}}, true
	}
	if code := runOnce(ctx, e.parts); code != 0 {
		t.Fatalf("code %d", code)
	}
	if len(bot.sent) != 2 || !strings.HasSuffix(bot.sent[1], "0 new files; nothing changed, no new snapshot") {
		t.Fatalf("sent %q", bot.sent)
	}
	runs, err := e.db.RunsSince(ctx, time.Time{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v %v", runs, err)
	}
	var rec runRecord
	_ = json.Unmarshal([]byte(runs[0].Summary), &rec)
	if rec.Update != 1 || !rec.Unchanged || len(rec.Snapshots) != 0 {
		t.Fatalf("record = %+v", rec)
	}
}
