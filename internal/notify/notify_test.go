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
