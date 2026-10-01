// Package notify sends run errors and periodic summaries through a Telegram
// bot (Bot API sendMessage); there is no long-running process.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Bot struct {
	Token   string
	ChatID  string
	BaseURL string // https://api.telegram.org by default
	HTTP    *http.Client
}

func (b Bot) Enabled() bool { return b.Token != "" && b.ChatID != "" }

func (b Bot) Send(ctx context.Context, text string) error {
	base := b.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	body, err := json.Marshal(map[string]any{"chat_id": b.ChatID, "text": text, "disable_web_page_preview": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+b.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := b.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// The URL holds the token: report the failure without it.
		return fmt.Errorf("send to the bot: request failed")
	}
	defer resp.Body.Close()
	var res struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if !res.OK {
		return fmt.Errorf("send to the bot: %s (HTTP %d)", res.Description, resp.StatusCode)
	}
	return nil
}

// RunResult is what an error message reports.
type RunResult struct {
	SyncErrors   []string
	BackupErrors []string
}

func ErrorText(host string, r RunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ tgbup on %s: the run had errors\n", host)
	// Telegram refuses messages over 4096 characters: show the first errors of
	// each section, each cut short, so the alert always goes through.
	section := func(title string, errs []string) {
		if len(errs) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", title)
		for i, e := range errs {
			if i == maxItems {
				fmt.Fprintf(&b, "… and %d more\n", len(errs)-maxItems)
				break
			}
			fmt.Fprintf(&b, "• %s\n", cut(e, maxItemLen))
		}
	}
	section("Sync", r.SyncErrors)
	section("Backup", r.BackupErrors)
	return cut(strings.TrimRight(b.String(), "\n"), maxMessage)
}

const (
	maxItems   = 10
	maxItemLen = 300
	maxMessage = 4000
)

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

type RepoTime struct {
	Repo string
	At   time.Time // zero: never
}

type Summary struct {
	Since        time.Time
	Runs         int
	FailedRuns   int
	NewMessages  int
	NewMedia     int
	ArchiveBytes int64
	GaveUpFiles  int
	LastBackup   []RepoTime
}

func SummaryText(host string, s Summary, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📦 tgbup on %s — since %s\n", host, s.Since.In(loc).Format("2006-01-02"))
	fmt.Fprintf(&b, "%d runs, %d failed\n", s.Runs, s.FailedRuns)
	fmt.Fprintf(&b, "+%s messages, +%s files\n", group(s.NewMessages), group(s.NewMedia))
	fmt.Fprintf(&b, "archive: %s\n", bytesText(s.ArchiveBytes))
	b.WriteString("last backup:\n")
	for _, r := range s.LastBackup {
		at := "never"
		if !r.At.IsZero() {
			at = r.At.In(loc).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "• %s: %s\n", r.Repo, at)
	}
	if s.GaveUpFiles > 0 {
		fmt.Fprintf(&b, "%d files could not be downloaded\n", s.GaveUpFiles)
	}
	return strings.TrimRight(b.String(), "\n")
}

func group(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}
