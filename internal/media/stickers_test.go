package media_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/media"
	"github.com/jounderwood/tgbup/internal/store"
)

func stickerRow(t *testing.T, id int, docID int64) store.Message {
	t.Helper()
	d := &tg.Document{ID: docID, AccessHash: 1, MimeType: "image/webp", Size: 30,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{Alt: "😺", Stickerset: &tg.InputStickerSetEmpty{}}}}
	mm := &tg.MessageMediaDocument{}
	mm.SetDocument(d)
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: chatN}, Date: 1}
	m.SetMedia(mm)
	raw, err := convert.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return store.Message{ChatID: chatID, ID: id, Date: 1, Raw: raw, TLLayer: tg.Layer, MediaKeys: []string{"document:" + strconv.FormatInt(docID, 10)}}
}

func (e *env) write(t *testing.T, rel string, n int) {
	t.Helper()
	p := filepath.Join(e.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// An archive from before stickers were kept as their emoji: their files go,
// downloads stopped half-way too, the messages get the emoji, and no other
// file is touched.
func TestOldStickerFilesGo(t *testing.T) {
	e := newEnv(t, 100)
	ctx := context.Background()
	broken := stickerRow(t, 23, 902)
	broken.Raw = []byte{1} // not a message tgbup can read: still a sticker
	outside := stickerRow(t, 24, 903)
	// 900 sent twice and downloaded; 901 half-way; 902, 903 never downloaded.
	if err := e.st.SaveBatch(ctx, store.Batch{ChatID: chatID,
		Messages: []store.Message{stickerRow(t, 20, 900), stickerRow(t, 21, 900), stickerRow(t, 22, 901), broken, outside},
		Media: []store.Media{{Key: "document:900", Kind: "sticker", Size: 30}, {Key: "document:901", Kind: "sticker", Size: 30},
			{Key: "document:902", Kind: "sticker", Size: 30}, {Key: "document:903", Kind: "sticker", Size: 30}}}); err != nil {
		t.Fatal(err)
	}
	done := filepath.Join("media", "1970", "sticker", "1970-01-01_000001_cat.webp")
	half := filepath.Join("media", "1970", "sticker", "1970-01-01_000001_dog.webp")
	e.write(t, done, 30)
	e.write(t, filepath.Base(media.PartPath(half)), 0) // a stray name that only looks like one
	e.write(t, media.PartPath(half), 10)
	e.write(t, filepath.Join("media", "1970", "photo", "keep.jpg"), 5)
	if err := e.st.MediaDone(ctx, "document:900", done); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.st.ReserveMediaPath(ctx, "document:901", half, media.PathKey(half)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// A record pointing out of the archive is never followed.
	e.write(t, "../outside.webp", 5)
	if ok, err := e.st.ReserveMediaPath(ctx, "document:903", "../outside.webp", "x"); err != nil || !ok {
		t.Fatal(ok, err)
	}

	if err := media.ForgetStickers(ctx, e.st, e.dir, e.d.Log); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(e.dir, filepath.Dir(done))) {
		t.Error("the sticker files or their folder are still there")
	}
	for _, keep := range []string{filepath.Join("media", "1970", "photo", "keep.jpg"), filepath.Base(media.PartPath(half)), "../outside.webp"} {
		if !exists(filepath.Join(e.dir, keep)) {
			t.Errorf("%s went", keep)
		}
	}
	if left, err := e.st.StickerFiles(ctx); err != nil || len(left) != 0 {
		t.Errorf("sticker records left: %+v %v", left, err)
	}
	ms, err := e.st.PageMessages(ctx, chatID, []int{20, 21, 22, 23, 24})
	if err != nil || len(ms) != 5 {
		t.Fatalf("messages = %+v %v", ms, err)
	}
	for _, m := range ms {
		want := `{"sticker":{"emoji":"😺"}}`
		if m.ID == 23 {
			want = `{"sticker":{}}`
		}
		if m.Media != nil || m.MetaJSON != want {
			t.Errorf("message %d: media %+v, meta %s", m.ID, m.Media, m.MetaJSON)
		}
	}
	// The downloads go on without them, and a second pass finds nothing.
	if r, err := e.d.Run(ctx); err != nil || r.Done != 1 {
		t.Errorf("downloads = %+v %v (the pdf only)", r, err)
	}
	if err := media.ForgetStickers(ctx, e.st, e.dir, e.d.Log); err != nil {
		t.Error(err)
	}
}

// A file that cannot be removed stays, and the stickers still become their
// emoji.
func TestStickerFileThatStaysDoesNotStopTheRest(t *testing.T) {
	e := newEnv(t, 100)
	ctx := context.Background()
	if err := e.st.SaveBatch(ctx, store.Batch{ChatID: chatID, Messages: []store.Message{stickerRow(t, 20, 900)},
		Media: []store.Media{{Key: "document:900", Kind: "sticker", Size: 30}}}); err != nil {
		t.Fatal(err)
	}
	// A folder where the file should be: os.Remove fails on it unless empty.
	stuck := filepath.Join("media", "1970", "sticker", "1970-01-01_000001_cat.webp")
	e.write(t, filepath.Join(stuck, "inside"), 1)
	if err := e.st.MediaDone(ctx, "document:900", stuck); err != nil {
		t.Fatal(err)
	}
	if err := media.ForgetStickers(ctx, e.st, e.dir, e.d.Log); err != nil {
		t.Fatal(err)
	}
	if ms, _ := e.st.PageMessages(ctx, chatID, []int{20}); len(ms) != 1 || ms[0].Media != nil || ms[0].MetaJSON == "" {
		t.Errorf("message = %+v", ms)
	}
	if _, err := os.Stat(filepath.Join(e.dir, stuck)); errors.Is(err, os.ErrNotExist) {
		t.Error("what could not be removed went")
	}
}
