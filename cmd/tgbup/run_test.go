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
	mu   sync.Mutex
	sent []string
	srv  *httptest.Server
}

func newFakeBot(t *testing.T) *fakeBot {
	b := &fakeBot{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		b.mu.Lock()
		b.sent = append(b.sent, m["text"].(string))
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
	for _, want := range []string{"backup: not configured", "notify: not configured"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
