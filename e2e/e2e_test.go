//go:build e2e

// Package e2e runs the real tgbup binary against Telegram's test servers.
// It needs a test-environment account logged in with `TGBUP_TEST_DC=1 tgbup login`.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	_ "modernc.org/sqlite"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var binPath string

func TestMain(m *testing.M) {
	if os.Getenv("TGBUP_TEST_DC") != "1" {
		fmt.Println("skipping e2e: run with TGBUP_TEST_DC=1 after `TGBUP_TEST_DC=1 tgbup login`")
		os.Exit(0)
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

func withTelegram(t *testing.T, fn func(ctx context.Context, raw *tg.Client) error) {
	t.Helper()
	p, err := config.ResolvePaths(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := config.LoadSecrets(p.Secrets, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := sec.RequireTelegram(); err != nil {
		t.Fatal(err)
	}
	opts := tgclient.Options{
		APIID: sec.APIID, APIHash: sec.APIHash, SessionPath: p.Session(true),
		Version: "e2e", MaxFloodWait: time.Minute, TestDC: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err = tgclient.Run(ctx, opts, func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		return fn(ctx, c.API())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func randomID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]))
}

func sentID(upd tg.UpdatesClass) (int, error) {
	switch u := upd.(type) {
	case *tg.UpdateShortSentMessage:
		return u.ID, nil
	case *tg.Updates:
		for _, x := range u.Updates {
			if m, ok := x.(*tg.UpdateMessageID); ok {
				return m.ID, nil
			}
		}
		for _, x := range u.Updates {
			if m, ok := x.(*tg.UpdateNewChannelMessage); ok {
				return m.Message.GetID(), nil
			}
		}
	}
	return 0, fmt.Errorf("no message id in %T", upd)
}

type fixture struct {
	title    string
	folder   string // Telegram caps folder titles at 12 characters
	channel  *tg.Channel
	peer     *tg.InputPeerChannel
	chatID   int64
	folderID int

	topicID, linkMsg, replyMsg, topicMsg, fwdMsg int
	docBytes                                     []byte
}

type seeder struct {
	raw  *tg.Client
	peer *tg.InputPeerChannel
}

func (s seeder) text(ctx context.Context, text string, entities []tg.MessageEntityClass, replyTo, topID int) (int, error) {
	req := &tg.MessagesSendMessageRequest{Peer: s.peer, Message: text, RandomID: randomID()}
	if len(entities) > 0 {
		req.SetEntities(entities)
	}
	if replyTo != 0 {
		r := &tg.InputReplyToMessage{ReplyToMsgID: replyTo}
		if topID != 0 {
			r.SetTopMsgID(topID)
		}
		req.SetReplyTo(r)
	}
	upd, err := s.raw.MessagesSendMessage(ctx, req)
	if err != nil {
		return 0, err
	}
	return sentID(upd)
}

func (s seeder) media(ctx context.Context, media tg.InputMediaClass) (int, error) {
	upd, err := s.raw.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: s.peer, Media: media, RandomID: randomID()})
	if err != nil {
		return 0, err
	}
	return sentID(upd)
}

func testPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := range 64 {
		for y := range 64 {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// seed builds a forum with every message kind the archive must keep and puts
// it into a fresh Telegram folder, which is how the config selects it.
func seed(ctx context.Context, raw *tg.Client, f *fixture) error {
	upd, err := raw.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{
		Megagroup: true, Forum: true, Title: f.title, About: "tgbup e2e, safe to delete",
	})
	if err != nil {
		return fmt.Errorf("create forum: %w", err)
	}
	if u, ok := upd.(*tg.Updates); ok {
		for _, c := range u.Chats {
			if ch, ok := c.(*tg.Channel); ok {
				f.channel = ch
			}
		}
	}
	if f.channel == nil {
		return fmt.Errorf("create forum: no channel in %T", upd)
	}
	f.peer = &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	f.chatID = convert.ChannelID(f.channel.ID)
	s := seeder{raw: raw, peer: f.peer}

	upd, err = raw.MessagesCreateForumTopic(ctx, &tg.MessagesCreateForumTopicRequest{Peer: f.peer, Title: "Рецепты", RandomID: randomID()})
	if err != nil {
		return fmt.Errorf("create topic: %w", err)
	}
	if f.topicID, err = sentID(upd); err != nil {
		return err
	}
	// "см. доку": the link covers "доку", UTF-16 offset 4, length 4.
	link := []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 4, Length: 4, URL: "https://example.com/doc"}}
	if f.linkMsg, err = s.text(ctx, "см. доку", link, 0, 0); err != nil {
		return fmt.Errorf("link message: %w", err)
	}
	if f.replyMsg, err = s.text(ctx, "ответ на ссылку", nil, f.linkMsg, 0); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	if f.topicMsg, err = s.text(ctx, "борщ: свёкла, капуста", nil, f.topicID, 0); err != nil {
		return fmt.Errorf("topic message: %w", err)
	}
	upd, err = raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer: f.peer, ToPeer: f.peer, ID: []int{f.linkMsg}, RandomID: []int64{randomID()},
	})
	if err != nil {
		return fmt.Errorf("forward: %w", err)
	}
	if f.fwdMsg, err = sentID(upd); err != nil {
		return err
	}

	up := uploader.NewUploader(raw)
	photo, err := up.FromBytes(ctx, "photo.png", testPNG())
	if err != nil {
		return fmt.Errorf("upload photo: %w", err)
	}
	if _, err := s.media(ctx, &tg.InputMediaUploadedPhoto{File: photo}); err != nil {
		return fmt.Errorf("send photo: %w", err)
	}
	f.docBytes = make([]byte, 700*1024) // spans two download chunks
	_, _ = rand.Read(f.docBytes)
	doc, err := up.FromBytes(ctx, "отчёт 2026.pdf", f.docBytes)
	if err != nil {
		return fmt.Errorf("upload document: %w", err)
	}
	if _, err := s.media(ctx, &tg.InputMediaUploadedDocument{
		File: doc, MimeType: "application/pdf",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "отчёт 2026.pdf"}},
	}); err != nil {
		return fmt.Errorf("send document: %w", err)
	}

	if _, err := raw.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{Peer: f.peer, ID: f.replyMsg, Silent: true}); err != nil {
		return fmt.Errorf("pin: %w", err)
	}

	filters, err := raw.MessagesGetDialogFilters(ctx)
	if err != nil {
		return fmt.Errorf("get folders: %w", err)
	}
	f.folderID = 2
	for _, fc := range filters.Filters {
		var id int
		switch v := fc.(type) {
		case *tg.DialogFilter:
			id = v.ID
		case *tg.DialogFilterChatlist:
			id = v.ID
		}
		if id >= f.folderID {
			f.folderID = id + 1
		}
	}
	req := &tg.MessagesUpdateDialogFilterRequest{ID: f.folderID}
	req.SetFilter(&tg.DialogFilter{ID: f.folderID, Title: tg.TextWithEntities{Text: f.folder}, IncludePeers: []tg.InputPeerClass{f.peer}})
	if _, err := raw.MessagesUpdateDialogFilter(ctx, req); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	return nil
}

func cleanup(ctx context.Context, raw *tg.Client, f *fixture) error {
	var errs []error
	if f.folderID != 0 {
		if _, err := raw.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: f.folderID}); err != nil {
			errs = append(errs, fmt.Errorf("delete folder: %w", err))
		}
	}
	if f.channel != nil {
		if _, err := raw.ChannelsDeleteChannel(ctx, &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}); err != nil {
			errs = append(errs, fmt.Errorf("delete forum: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%v", errs)
	}
	return nil
}

func runSync(t *testing.T, cfgPath string) {
	t.Helper()
	cmd := exec.Command(binPath, "sync")
	cmd.Env = append(os.Environ(), "TGBUP_CONFIG="+cfgPath, "TGBUP_TEST_DC=1")
	out, err := cmd.CombinedOutput()
	t.Logf("tgbup sync:\n%s", out)
	if err != nil {
		t.Fatalf("tgbup sync failed: %v", err)
	}
}

func scalar[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("%s %v: %v", query, args, err)
	}
	return v
}

func TestSyncArchivesForum(t *testing.T) {
	now := time.Now().Unix()
	f := &fixture{title: fmt.Sprintf("tgbup-e2e-%d", now), folder: fmt.Sprintf("e2e-%d", now%100000)}
	t.Cleanup(func() {
		withTelegram(t, func(ctx context.Context, raw *tg.Client) error { return cleanup(ctx, raw, f) })
	})
	withTelegram(t, func(ctx context.Context, raw *tg.Client) error { return seed(ctx, raw, f) })

	dir := t.TempDir()
	archive := filepath.Join(dir, "archive")
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf("archive_dir: %s\ntargets:\n  folders: [%q]\n", archive, f.folder)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	runSync(t, cfgPath)

	db, err := sql.Open("sqlite", filepath.Join(archive, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if typ := scalar[string](t, db, `SELECT type FROM chats WHERE id = ?`, f.chatID); typ != "forum" {
		t.Errorf("chat type = %s, want forum", typ)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM topics WHERE chat_id = ? AND topic_id = ? AND title = 'Рецепты'`, f.chatID, f.topicID); n != 1 {
		t.Errorf("topic not archived")
	}
	msg := func(col string, id int) sql.NullString {
		return scalar[sql.NullString](t, db, `SELECT `+col+` FROM messages WHERE chat_id = ? AND id = ?`, f.chatID, id)
	}
	if e := msg("entities_json", f.linkMsg); !bytes.Contains([]byte(e.String), []byte(`"url":"https://example.com/doc"`)) {
		t.Errorf("hidden link lost: %q", e.String)
	}
	if r := msg("reply_to_id", f.replyMsg); r.String != fmt.Sprint(f.linkMsg) {
		t.Errorf("reply_to_id = %q, want %d", r.String, f.linkMsg)
	}
	if tp := msg("topic_id", f.topicMsg); tp.String != fmt.Sprint(f.topicID) {
		t.Errorf("topic_id = %q, want %d", tp.String, f.topicID)
	}
	if fw := msg("fwd_from_json", f.fwdMsg); !fw.Valid {
		t.Errorf("forward header lost")
	}
	if p := msg("pinned", f.replyMsg); p.String != "1" {
		t.Errorf("pinned = %q", p.String)
	}
	for _, action := range []string{"messageActionTopicCreate", "messageActionPinMessage"} {
		n := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ? AND service_action_json LIKE ?`, f.chatID, `%`+action+`%`)
		if n == 0 {
			t.Errorf("service message %s not archived", action)
		}
	}

	if n := scalar[int](t, db, `SELECT count(*) FROM media WHERE status = 'done'`); n != 2 {
		t.Errorf("media done = %d, want 2 (photo + document)", n)
	}
	docPath := scalar[string](t, db, `SELECT local_path FROM media WHERE kind = 'document'`)
	got, err := os.ReadFile(filepath.Join(archive, docPath))
	if err != nil || !bytes.Equal(got, f.docBytes) {
		t.Errorf("document bytes differ from the uploaded file (err=%v, %d vs %d bytes)", err, len(got), len(f.docBytes))
	}
	photoPath := scalar[string](t, db, `SELECT local_path FROM media WHERE kind = 'photo'`)
	if st, err := os.Stat(filepath.Join(archive, photoPath)); err != nil || st.Size() == 0 {
		t.Errorf("photo file missing: %v", err)
	}

	before := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ?`, f.chatID)
	withTelegram(t, func(ctx context.Context, raw *tg.Client) error {
		s := seeder{raw: raw, peer: f.peer}
		for i := range 3 {
			if _, err := s.text(ctx, fmt.Sprintf("новое %d", i), nil, 0, 0); err != nil {
				return err
			}
		}
		return nil
	})
	runSync(t, cfgPath)
	if after := scalar[int](t, db, `SELECT count(*) FROM messages WHERE chat_id = ?`, f.chatID); after != before+3 {
		t.Errorf("incremental sync: %d → %d messages, want +3", before, after)
	}
}
