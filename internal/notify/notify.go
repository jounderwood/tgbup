// Package notify sends run errors, periodic summaries and, quietly, what
// tgbup is doing (a run started, finished, setup done) through a Telegram bot
// (Bot API sendMessage); there is no long-running process.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func (b Bot) Send(ctx context.Context, text string) error { return b.send(ctx, text, false) }

// SendQuiet sends without a sound: routine news (a run started or finished)
// must not ring several times a day.
func (b Bot) SendQuiet(ctx context.Context, text string) error { return b.send(ctx, text, true) }

func (b Bot) send(ctx context.Context, text string, quiet bool) error {
	err := b.call(ctx, "sendMessage", map[string]any{"chat_id": b.ChatID, "text": text,
		"disable_web_page_preview": true, "disable_notification": quiet}, nil)
	if err != nil {
		return fmt.Errorf("send to the bot: %w", err)
	}
	return nil
}

// StartText says a run began: by the schedule, from the page, or the first
// download (through a Telegram data export with takeout).
func StartText(host, trigger string, takeout bool) string {
	why := map[string]string{"schedule": "scheduled", "page": "started from the page", "init": "started by tgbup init"}[trigger]
	if why == "" {
		why = "started by hand"
	}
	what := "update"
	if takeout {
		what = "download through a Telegram data export"
	}
	return fmt.Sprintf("▶ tgbup on %s: %s %s", host, what, why)
}

// DoneText says a run finished: what it brought and the snapshots it made
// ("~/Tgbup/backup 3333bbbb"); unchanged places already held the archive as it is.
// Counts are what a run brought: messages new, edited and deleted in
// Telegram (kept in the archive), and files downloaded.
type Counts struct{ New, Edited, Deleted, Files int }

func DoneText(host string, c Counts, snapshots, unchanged []string) string {
	text := fmt.Sprintf("✓ tgbup on %s: %d new messages", host, c.New)
	if c.Edited > 0 {
		text += fmt.Sprintf(", %d edited", c.Edited)
	}
	if c.Deleted > 0 {
		text += fmt.Sprintf(", %d deleted in Telegram", c.Deleted)
	}
	text += fmt.Sprintf(", %d new files", c.Files)
	switch {
	case len(snapshots) == 0 && len(unchanged) == 0:
		return text + "; no backup set up"
	case len(snapshots) == 0:
		return text + "; nothing changed, no new snapshot"
	}
	text += "; snapshot " + strings.Join(snapshots, ", ")
	if len(unchanged) > 0 {
		text += "; already up to date: " + strings.Join(unchanged, ", ")
	}
	return text
}

// SetupText says tgbup is set up and what it will do.
func SetupText(host string, chats int, folders, places []string, times string) string {
	what := fmt.Sprintf("%d chats", chats)
	if len(folders) > 0 {
		what += " and the folders " + strings.Join(folders, ", ")
	}
	backups := "off"
	if len(places) > 0 {
		backups = strings.Join(places, ", ")
	}
	return fmt.Sprintf("✅ tgbup on %s is set up\nArchiving: %s\nBackups: %s\nRuns at: %s", host, what, backups, times)
}

// Me checks the token and returns the bot's @username.
func (b Bot) Me(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := b.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return "", fmt.Errorf("check the bot token: %w", err)
	}
	return me.Username, nil
}

// Chat is a private chat someone opened with the bot.
type Chat struct {
	ID   int64
	Name string // first and last name, and @username when set
}

// PrivateChat returns the latest private chat that wrote to the bot, waiting
// for one until ctx ends; poll is the long-poll timeout of each request.
func (b Bot) PrivateChat(ctx context.Context, poll time.Duration) (Chat, error) {
	offset := 0
	for {
		var updates []struct {
			UpdateID int `json:"update_id"`
			Message  *struct {
				Chat struct {
					ID        int64  `json:"id"`
					Type      string `json:"type"`
					FirstName string `json:"first_name"`
					LastName  string `json:"last_name"`
					Username  string `json:"username"`
				} `json:"chat"`
			} `json:"message"`
		}
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(poll.Seconds()), "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return Chat{}, ctx.Err()
			}
			return Chat{}, fmt.Errorf("read the bot's messages: %w", err)
		}
		var found *Chat
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Chat.Type != "private" {
				continue
			}
			c := u.Message.Chat
			name := strings.TrimSpace(c.FirstName + " " + c.LastName)
			if c.Username != "" {
				name = strings.TrimSpace(name + " @" + c.Username)
			}
			found = &Chat{ID: c.ID, Name: name}
		}
		if found != nil {
			return *found, nil
		}
		if err := ctx.Err(); err != nil {
			return Chat{}, err
		}
	}
}

// call invokes a Bot API method; out receives the result field.
func (b Bot) call(ctx context.Context, method string, params map[string]any, out any) error {
	base := b.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+b.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := b.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// The URL holds the token: report the failure without it.
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	var res struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if !res.OK {
		return fmt.Errorf("%s (HTTP %d)", res.Description, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(res.Result, out)
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
