//go:build e2e

// Package testenv seeds and removes e2e data on Telegram's test servers.
// Everything here is pinned to the test environment and its own session file.
package testenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

// Fixture records what Seed created, so the run and the cleanup can be separate steps.
type Fixture struct {
	Title      string `json:"title"`
	Folder     string `json:"folder"`
	FolderID   int    `json:"folder_id"`
	ChannelID  int64  `json:"channel_id"`
	AccessHash int64  `json:"access_hash"`
	ChatID     int64  `json:"chat_id"`

	TopicID  int `json:"topic_id"`
	LinkMsg  int `json:"link_msg"`
	ReplyMsg int `json:"reply_msg"`
	TopicMsg int `json:"topic_msg"`
	FwdMsg   int `json:"fwd_msg"`

	DocSHA256 string `json:"doc_sha256"`
	DocSize   int    `json:"doc_size"`
}

func (f Fixture) Peer() *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: f.ChannelID, AccessHash: f.AccessHash}
}

// FixturePath is e2e/.fixture.json regardless of the caller's working directory.
func FixturePath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", ".fixture.json")
}

func LoadFixture() (Fixture, error) {
	var f Fixture
	b, err := os.ReadFile(FixturePath())
	if err != nil {
		return f, fmt.Errorf("no e2e fixture (run `make e2e-seed` first): %w", err)
	}
	return f, json.Unmarshal(b, &f)
}

func SaveFixture(f Fixture) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(FixturePath(), append(b, '\n'), 0o600)
}

func PinTestServers() error { return os.Setenv("TGBUP_TEST_DC", "1") }

// WithTelegram connects to the test servers with the test-server session.
func WithTelegram(fn func(ctx context.Context, raw *tg.Client) error) error {
	if err := PinTestServers(); err != nil {
		return err
	}
	p, err := config.ResolvePaths(os.Getenv)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Session(true)); err != nil {
		return fmt.Errorf("no test-server session at %s: run `TGBUP_TEST_DC=1 bin/tgbup login` first", p.Session(true))
	}
	sec, err := config.LoadSecrets(p.Secrets, os.Getenv)
	if err != nil {
		return err
	}
	if err := sec.RequireTelegram(); err != nil {
		return err
	}
	opts := tgclient.Options{
		APIID: sec.APIID, APIHash: sec.APIHash, SessionPath: p.Session(true),
		Version: "e2e", MaxFloodWait: time.Minute, TestDC: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return tgclient.Run(ctx, opts, func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		return fn(ctx, c.API())
	})
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

// SendText posts into the fixture forum; replyTo 0 posts to General.
func SendText(ctx context.Context, raw *tg.Client, peer tg.InputPeerClass, text string, entities []tg.MessageEntityClass, replyTo int) (int, error) {
	req := &tg.MessagesSendMessageRequest{Peer: peer, Message: text, RandomID: randomID()}
	if len(entities) > 0 {
		req.SetEntities(entities)
	}
	if replyTo != 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	}
	upd, err := raw.MessagesSendMessage(ctx, req)
	if err != nil {
		return 0, err
	}
	return sentID(upd)
}

func sendMedia(ctx context.Context, raw *tg.Client, peer tg.InputPeerClass, media tg.InputMediaClass) error {
	_, err := raw.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: peer, Media: media, RandomID: randomID()})
	return err
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

// Seed builds a forum with every message kind the archive must keep and puts
// it into a fresh Telegram folder, which is how the e2e config selects it.
// On error the partial fixture is returned so Cleanup can remove what exists.
func Seed(ctx context.Context, raw *tg.Client) (Fixture, error) {
	now := time.Now().UnixNano()
	// Telegram caps folder titles at 12 characters.
	f := Fixture{Title: fmt.Sprintf("tgbup-e2e-%d", now), Folder: fmt.Sprintf("e2e-%d", now%1000000)}

	upd, err := raw.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{
		Megagroup: true, Forum: true, Title: f.Title, About: "tgbup e2e, safe to delete",
	})
	if err != nil {
		return f, fmt.Errorf("create forum: %w", err)
	}
	if u, ok := upd.(*tg.Updates); ok {
		for _, c := range u.Chats {
			if ch, ok := c.(*tg.Channel); ok {
				f.ChannelID, f.AccessHash = ch.ID, ch.AccessHash
			}
		}
	}
	if f.ChannelID == 0 {
		return f, fmt.Errorf("create forum: no channel in %T", upd)
	}
	f.ChatID = convert.ChannelID(f.ChannelID)
	peer := f.Peer()

	upd, err = raw.MessagesCreateForumTopic(ctx, &tg.MessagesCreateForumTopicRequest{Peer: peer, Title: "Рецепты", RandomID: randomID()})
	if err != nil {
		return f, fmt.Errorf("create topic: %w", err)
	}
	if f.TopicID, err = sentID(upd); err != nil {
		return f, err
	}
	// "см. доку": the link covers "доку", UTF-16 offset 4, length 4.
	link := []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 4, Length: 4, URL: "https://example.com/doc"}}
	if f.LinkMsg, err = SendText(ctx, raw, peer, "см. доку", link, 0); err != nil {
		return f, fmt.Errorf("link message: %w", err)
	}
	if f.ReplyMsg, err = SendText(ctx, raw, peer, "ответ на ссылку", nil, f.LinkMsg); err != nil {
		return f, fmt.Errorf("reply: %w", err)
	}
	if f.TopicMsg, err = SendText(ctx, raw, peer, "борщ: свёкла, капуста", nil, f.TopicID); err != nil {
		return f, fmt.Errorf("topic message: %w", err)
	}
	upd, err = raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer: peer, ToPeer: peer, ID: []int{f.LinkMsg}, RandomID: []int64{randomID()},
	})
	if err != nil {
		return f, fmt.Errorf("forward: %w", err)
	}
	if f.FwdMsg, err = sentID(upd); err != nil {
		return f, err
	}

	up := uploader.NewUploader(raw)
	photo, err := up.FromBytes(ctx, "photo.png", testPNG())
	if err != nil {
		return f, fmt.Errorf("upload photo: %w", err)
	}
	if err := sendMedia(ctx, raw, peer, &tg.InputMediaUploadedPhoto{File: photo}); err != nil {
		return f, fmt.Errorf("send photo: %w", err)
	}
	docBytes := make([]byte, 700*1024) // spans two download chunks
	_, _ = rand.Read(docBytes)
	sum := sha256.Sum256(docBytes)
	f.DocSHA256, f.DocSize = hex.EncodeToString(sum[:]), len(docBytes)
	doc, err := up.FromBytes(ctx, "отчёт 2026.pdf", docBytes)
	if err != nil {
		return f, fmt.Errorf("upload document: %w", err)
	}
	if err := sendMedia(ctx, raw, peer, &tg.InputMediaUploadedDocument{
		File: doc, MimeType: "application/pdf",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "отчёт 2026.pdf"}},
	}); err != nil {
		return f, fmt.Errorf("send document: %w", err)
	}

	if _, err := raw.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{Peer: peer, ID: f.ReplyMsg, Silent: true}); err != nil {
		return f, fmt.Errorf("pin: %w", err)
	}

	filters, err := raw.MessagesGetDialogFilters(ctx)
	if err != nil {
		return f, fmt.Errorf("get folders: %w", err)
	}
	folderID := 2
	for _, fc := range filters.Filters {
		var id int
		switch v := fc.(type) {
		case *tg.DialogFilter:
			id = v.ID
		case *tg.DialogFilterChatlist:
			id = v.ID
		}
		if id >= folderID {
			folderID = id + 1
		}
	}
	req := &tg.MessagesUpdateDialogFilterRequest{ID: folderID}
	req.SetFilter(&tg.DialogFilter{ID: folderID, Title: tg.TextWithEntities{Text: f.Folder}, IncludePeers: []tg.InputPeerClass{peer}})
	if _, err := raw.MessagesUpdateDialogFilter(ctx, req); err != nil {
		return f, fmt.Errorf("create folder: %w", err)
	}
	f.FolderID = folderID
	return f, nil
}

func Cleanup(ctx context.Context, raw *tg.Client, f Fixture) error {
	var errs []error
	if f.FolderID != 0 {
		if _, err := raw.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: f.FolderID}); err != nil {
			errs = append(errs, fmt.Errorf("delete folder: %w", err))
		}
	}
	if f.ChannelID != 0 {
		if _, err := raw.ChannelsDeleteChannel(ctx, &tg.InputChannel{ChannelID: f.ChannelID, AccessHash: f.AccessHash}); err != nil {
			errs = append(errs, fmt.Errorf("delete forum: %w", err))
		}
	}
	return errors.Join(errs...)
}
