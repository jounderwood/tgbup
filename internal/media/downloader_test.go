package media_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/media"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

const chatN = int64(1)

var chatID = convert.ChannelID(chatN)

type fakeFiles struct {
	mu        sync.Mutex
	data      map[int64][]byte
	offsets   []int64
	fail      error
	expired   bool // FILE_REFERENCE_EXPIRED until the reference is "fresh"
	onChunk   func(n int)
	refetches int
}

func (f *fakeFiles) FileChunk(ctx context.Context, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.fail != nil {
		return nil, f.fail
	}
	d := loc.(*tg.InputDocumentFileLocation)
	if f.expired && string(d.FileReference) != "fresh" {
		return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
	}
	f.offsets = append(f.offsets, offset)
	if f.onChunk != nil {
		f.onChunk(len(f.offsets))
	}
	b := f.data[d.ID]
	if offset >= int64(len(b)) {
		return nil, nil
	}
	end := min(offset+int64(limit), int64(len(b)))
	return b[offset:end], nil
}

func (f *fakeFiles) Message(_ context.Context, _ tg.InputPeerClass, id int) (tg.MessageClass, error) {
	f.refetches++
	return docMessage(id, 555, "fresh", int64(len(f.data[555]))), nil
}

func (f *fakeFiles) HistoryBefore(context.Context, tg.InputPeerClass, int, int) (tgclient.Page, error) {
	return tgclient.Page{}, errors.New("unused")
}

func (f *fakeFiles) HistoryAfter(context.Context, tg.InputPeerClass, int, int) (tgclient.Page, error) {
	return tgclient.Page{}, errors.New("unused")
}

func (f *fakeFiles) PinnedIDs(context.Context, tg.InputPeerClass) ([]int, error) { return nil, nil }

func (f *fakeFiles) ForumTopics(context.Context, tg.InputPeerClass) ([]tgclient.Topic, error) {
	return nil, nil
}

func docMessage(id int, docID int64, ref string, size int64) *tg.Message {
	d := &tg.Document{ID: docID, AccessHash: 1, FileReference: []byte(ref), MimeType: "application/pdf", Size: size,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "../../отчёт 2026.pdf"}}}
	mm := &tg.MessageMediaDocument{}
	mm.SetDocument(d)
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: chatN}, Date: 1}
	m.SetMedia(mm)
	return m
}

type env struct {
	st   *store.Store
	dir  string
	tg   *fakeFiles
	d    *media.Downloader
	data []byte
}

func newEnv(t *testing.T, size int) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.UpsertChat(ctx, store.Chat{ID: chatID, Type: store.ChatSupergroup}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(rand.IntN(256))
	}
	res, _, err := convert.Message(docMessage(7, 555, "old", int64(size)), convert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBatch(ctx, store.Batch{ChatID: chatID, Messages: []store.Message{res.Message}, Media: res.Media}); err != nil {
		t.Fatal(err)
	}
	f := &fakeFiles{data: map[int64][]byte{555: data}}
	return &env{st: st, dir: dir, tg: f, data: data, d: &media.Downloader{
		TG: f, Store: st, ArchiveDir: dir, Workers: 2,
		Peers: map[int64]tg.InputPeerClass{chatID: &tg.InputPeerChannel{ChannelID: chatN}},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
}

func (e *env) final() string { return filepath.Join(e.dir, "media", "document", "555.pdf") }

func (e *env) info(t *testing.T) store.MediaInfo {
	t.Helper()
	i, err := e.st.MediaInfo(context.Background(), "document:555")
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestDownloadsMultiChunkFile(t *testing.T) {
	e := newEnv(t, media.ChunkSize*2+1234)
	r, err := e.d.Run(context.Background())
	if err != nil || r.Done != 1 {
		t.Fatalf("report = %+v %v", r, err)
	}
	got, err := os.ReadFile(e.final())
	if err != nil || !bytes.Equal(got, e.data) {
		t.Fatalf("file mismatch: %v", err)
	}
	if i := e.info(t); i.Status != store.MediaDone || i.LocalPath != filepath.Join("media", "document", "555.pdf") {
		t.Fatalf("info = %+v", i)
	}
}

func TestResumesFromAlignedPartFile(t *testing.T) {
	e := newEnv(t, media.ChunkSize*3)
	if err := os.MkdirAll(filepath.Dir(e.final()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.final()+".part", e.data[:media.ChunkSize+1000], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.tg.offsets[0] != media.ChunkSize {
		t.Fatalf("first offset = %d, want %d", e.tg.offsets[0], media.ChunkSize)
	}
	got, _ := os.ReadFile(e.final())
	if !bytes.Equal(got, e.data) {
		t.Fatal("resumed file differs")
	}
}

func TestSkipsByKindAndSize(t *testing.T) {
	e := newEnv(t, 100)
	e.d.SkipKinds = []string{"document"}
	r, _ := e.d.Run(context.Background())
	if r.Skipped != 1 || e.info(t).Status != store.MediaSkipped {
		t.Fatalf("kind skip: %+v %+v", r, e.info(t))
	}

	e = newEnv(t, 100)
	e.d.MaxFileSize = 50
	r, _ = e.d.Run(context.Background())
	if r.Skipped != 1 || !strings.Contains(e.info(t).Error, "max_file_size") {
		t.Fatalf("size skip: %+v %+v", r, e.info(t))
	}
}

func TestSkippedMediaIsRequeuedWhenConfigAllowsIt(t *testing.T) {
	e := newEnv(t, 100)
	e.d.MaxFileSize = 50
	if r, _ := e.d.Run(context.Background()); r.Skipped != 1 {
		t.Fatalf("first run = %+v", r)
	}
	e.d.MaxFileSize = 0
	r, err := e.d.Run(context.Background())
	if err != nil || r.Done != 1 || e.info(t).Status != store.MediaDone {
		t.Fatalf("after raising the limit: %+v %+v %v", r, e.info(t), err)
	}
}

func TestLocalDiskErrorsDoNotBurnAttempts(t *testing.T) {
	e := newEnv(t, 1000)
	readOnly := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
	e.d.ArchiveDir = readOnly

	r, err := e.d.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.LocalErr == nil || r.Failed != 0 {
		t.Fatalf("report = %+v", r)
	}
	if i := e.info(t); i.Status != store.MediaPending || i.Attempts != 0 {
		t.Fatalf("local error counted as a download attempt: %+v", i)
	}
}

func TestExpiredFileReferenceIsRefreshed(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.expired = true
	r, err := e.d.Run(context.Background())
	if err != nil || r.Done != 1 || e.tg.refetches != 1 {
		t.Fatalf("report = %+v refetches=%d err=%v", r, e.tg.refetches, err)
	}
}

func TestFailuresBecomeFailedAfterMaxAttempts(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.fail = errors.New("boom")
	for i := 0; i < media.MaxAttempts; i++ {
		if _, err := e.d.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if i := e.info(t); i.Status != store.MediaFailed || i.Attempts != media.MaxAttempts {
		t.Fatalf("info = %+v", i)
	}
	e.tg.fail = nil
	r, _ := e.d.Run(context.Background())
	if r.Done+r.Failed+r.Skipped+r.Deferred != 0 {
		t.Fatalf("failed media retried: %+v", r)
	}
}

func TestFloodAndMissingPeerAreDeferredWithoutAttempts(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.fail = tgerr.New(420, "FLOOD_WAIT_900")
	r, _ := e.d.Run(context.Background())
	if r.Deferred != 1 || e.info(t).Attempts != 0 || e.info(t).Status != store.MediaPending {
		t.Fatalf("flood: %+v %+v", r, e.info(t))
	}

	e.tg.fail = nil
	e.d.Peers = map[int64]tg.InputPeerClass{}
	r, _ = e.d.Run(context.Background())
	if r.Deferred != 1 || e.info(t).Status != store.MediaPending {
		t.Fatalf("missing peer: %+v %+v", r, e.info(t))
	}
}

func TestCancelLeavesPartAndPending(t *testing.T) {
	e := newEnv(t, media.ChunkSize*3)
	ctx, cancel := context.WithCancel(context.Background())
	e.tg.onChunk = func(n int) {
		if n == 1 {
			cancel()
		}
	}
	r, _ := e.d.Run(ctx)
	if r.Failed != 0 {
		t.Fatalf("cancel counted as failure: %+v", r)
	}
	if i := e.info(t); i.Status != store.MediaPending || i.Attempts != 0 {
		t.Fatalf("info = %+v", i)
	}
	st, err := os.Stat(e.final() + ".part")
	if err != nil || st.Size() != media.ChunkSize {
		t.Fatalf("part = %v %v", st, err)
	}
}

func TestRelPathIgnoresHostileFileName(t *testing.T) {
	got := media.RelPath(store.MediaJob{Key: "document:555", Kind: "document", FileName: "../../etc/passwd", Mime: "application/pdf"})
	if got != filepath.Join("media", "document", "555.pdf") {
		t.Fatalf("path = %s", got)
	}
	if e := media.Ext("x.tar.GZ", "", "document"); e != ".gz" {
		t.Errorf("ext = %s", e)
	}
	if e := media.Ext("evil.p;h p", "", "voice"); e != ".ogg" {
		t.Errorf("unsafe ext accepted: %s", e)
	}
}
