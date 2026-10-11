package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123:abc/sendMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	b := Bot{Token: "123:abc", ChatID: "42", BaseURL: srv.URL}
	if !b.Enabled() {
		t.Fatal("bot with token and chat is disabled")
	}
	if err := b.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "42" || got["text"] != "hello" {
		t.Fatalf("request = %v", got)
	}
}

func TestSendReportsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()
	err := Bot{Token: "1:a", ChatID: "x", BaseURL: srv.URL}.Send(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "1:a") {
		t.Fatalf("err = %v (must name the problem and never the token)", err)
	}
}

func TestDisabledWithoutToken(t *testing.T) {
	if (Bot{ChatID: "1"}).Enabled() || (Bot{Token: "1:a"}).Enabled() {
		t.Fatal("a bot without token or chat is enabled")
	}
}

func TestErrorText(t *testing.T) {
	got := ErrorText("mac", RunResult{
		SyncErrors:   []string{"chat Family: FLOOD_WAIT_600"},
		BackupErrors: []string{"rclone:gdrive:tgbup-restic: restic backup: unauthorized"},
	})
	for _, want := range []string{"⚠️ tgbup on mac", "chat Family: FLOOD_WAIT_600", "rclone:gdrive:tgbup-restic: restic backup: unauthorized"} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}
}

func TestSummaryText(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	got := SummaryText("mac", Summary{
		Since: now.Add(-7 * 24 * time.Hour), Runs: 28, FailedRuns: 1, NewMessages: 1234, NewMedia: 56,
		ArchiveBytes: 3 << 30, GaveUpFiles: 2,
		LastBackup: []RepoTime{{Repo: "~/iCloud/tgbup-restic", At: now.Add(-3 * time.Hour)}, {Repo: "rclone:gdrive:tgbup-restic"}},
	}, time.UTC)
	for _, want := range []string{"📦 tgbup on mac", "since 2026-10-02", "28 runs, 1 failed", "+1 234 messages", "+56 files",
		"3.0 GB", "~/iCloud/tgbup-restic: 2026-10-09 09:00", "rclone:gdrive:tgbup-restic: never", "2 files could not be downloaded"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

// Telegram rejects messages over 4096 characters; a run with hundreds of
// errors must still get its alert through.
func TestErrorTextFitsTelegram(t *testing.T) {
	var errs []string
	for i := range 500 {
		errs = append(errs, "media document:"+strings.Repeat("9", 10)+" failed with a long reason "+strings.Repeat("x", 40)+" #"+string(rune('a'+i%26)))
	}
	got := ErrorText("mac", RunResult{SyncErrors: errs, BackupErrors: []string{"icloud: disk full"}})
	if n := len([]rune(got)); n > 4000 {
		t.Fatalf("message is %d characters", n)
	}
	for _, want := range []string{"… and 490 more", "icloud: disk full"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q", want)
		}
	}
}

func TestMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bot1:good/getMe" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":7,"is_bot":true,"username":"family_backup_bot"}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer srv.Close()
	name, err := Bot{Token: "1:good", BaseURL: srv.URL}.Me(context.Background())
	if err != nil || name != "family_backup_bot" {
		t.Fatalf("Me = %q, %v", name, err)
	}
	if _, err := (Bot{Token: "1:bad", BaseURL: srv.URL}).Me(context.Background()); err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("bad token: %v", err)
	}
}

func TestPrivateChatSkipsGroupsAndWaits(t *testing.T) {
	var offsets []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		offsets = append(offsets, req["offset"].(float64))
		switch len(offsets) {
		case 1: // a group message only
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":10,"message":{"chat":{"id":-5,"type":"group","title":"g"}}}]}`))
		case 2: // nothing yet
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":11,"message":{"chat":{"id":99,"type":"private","first_name":"Ann","username":"ann"}}}]}`))
		}
	}))
	defer srv.Close()
	c, err := Bot{Token: "1:a", BaseURL: srv.URL}.PrivateChat(context.Background(), 0)
	if err != nil || c.ID != 99 || c.Name != "Ann @ann" {
		t.Fatalf("chat = %+v, %v", c, err)
	}
	if offsets[0] != 0 || offsets[1] != 11 {
		t.Errorf("offsets = %v: seen updates must not come back", offsets)
	}
}

func TestPrivateChatStopsWithContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := (Bot{Token: "1:a", BaseURL: srv.URL}).PrivateChat(ctx, 0); err == nil {
		t.Fatal("no error after the context ended")
	}
}

func TestProgressTexts(t *testing.T) {
	if got := StartText("mac", "schedule", false); got != "▶ tgbup on mac: update scheduled" {
		t.Errorf("start = %q", got)
	}
	if got := DoneText("mac", 3, 0, 1, nil, nil); !strings.Contains(got, "no backup set up") {
		t.Errorf("done without backups = %q", got)
	}
	if got := DoneText("mac", 2, 3, 0, []string{"x 1"}, nil); !strings.Contains(got, ": 2 new messages, 3 edited, 0 new files") {
		t.Errorf("done with edits = %q", got)
	}
	if got := DoneText("mac", 0, 0, 0, nil, []string{"~/Tgbup/backup"}); !strings.HasSuffix(got, "0 new files; nothing changed, no new snapshot") {
		t.Errorf("done, unchanged = %q", got)
	}
	if got := DoneText("mac", 2, 0, 0, []string{"gdrive:tgbup 3333bbbb"}, []string{"~/Tgbup/backup"}); !strings.Contains(got, "; snapshot gdrive:tgbup 3333bbbb; already up to date: ~/Tgbup/backup") {
		t.Errorf("done, one place behind = %q", got)
	}
	got := SetupText("mac", 4, []string{"Семья"}, []string{"~/Tgbup/backup", "iCloud"}, "06:30, 18:30")
	for _, want := range []string{"is set up", "4 chats and the folders Семья", "Backups: ~/Tgbup/backup, iCloud", "Runs at: 06:30, 18:30"} {
		if !strings.Contains(got, want) {
			t.Errorf("setup text lacks %q:\n%s", want, got)
		}
	}
}
