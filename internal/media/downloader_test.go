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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/sys/unix"

	"github.com/joeunderwood/tgbup/internal/convert"
	"github.com/joeunderwood/tgbup/internal/media"
	"github.com/joeunderwood/tgbup/internal/store"
	"github.com/joeunderwood/tgbup/internal/tgclient"
)

const chatN = int64(1)

var chatID = convert.ChannelID(chatN)

type fakeFiles struct {
	mu        sync.Mutex
	data      map[int64][]byte
	offsets   []int64
	fail      error
	hang      bool // wait for the context like a request to an unreachable data center
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
	if f.hang {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, ctx.Err()
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
		Peers:    map[int64]tg.InputPeerClass{chatID: &tg.InputPeerChannel{ChannelID: chatN}},
		Location: time.UTC,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
}

// finalRel is where docMessage's file goes: its date is 1 second after the epoch.
var finalRel = filepath.Join("media", "1970", "file", "1970-01-01_000001_отчёт 2026.pdf")

func (e *env) final() string { return filepath.Join(e.dir, finalRel) }

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
	if i := e.info(t); i.Status != store.MediaDone || i.LocalPath != finalRel {
		t.Fatalf("info = %+v", i)
	}
	if st, _ := os.Stat(e.final()); !st.ModTime().Equal(time.Unix(1, 0)) {
		t.Errorf("modified %v, want the message date", st.ModTime())
	}
}

func TestSomeoneElsesFileIsQuarantined(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("quarantine is a macOS attribute")
	}
	e := newEnv(t, 100)
	if _, err := e.d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, err := unix.Getxattr(e.final(), "com.apple.quarantine", buf)
	if err != nil || !strings.Contains(string(buf[:n]), ";tgbup;") {
		t.Fatalf("quarantine = %q %v", buf[:n], err)
	}
}

func TestOwnFileIsNotQuarantined(t *testing.T) {
	for name, sent := range map[string]func(*tg.Message){
		"outgoing":    func(m *tg.Message) { m.Out = true },
		"from self":   func(m *tg.Message) { m.SetFromID(&tg.PeerUser{UserID: 42}) },
		"saved, bare": func(m *tg.Message) { m.PeerID = &tg.PeerUser{UserID: 42} },
	} {
		if quarantinedWhenSent(t, sent) {
			t.Errorf("%s: the owner's own file was quarantined", name)
		}
	}
}

func TestForwardedFileIsSomeoneElses(t *testing.T) {
	for name, sent := range map[string]func(*tg.Message){
		"forwarded": func(m *tg.Message) {
			m.Out = true
			m.SetFwdFrom(tg.MessageFwdHeader{Date: 1, FromName: "someone"})
		},
		"via an inline bot": func(m *tg.Message) { m.Out = true; m.SetViaBotID(7) },
	} {
		if !quarantinedWhenSent(t, sent) {
			t.Errorf("%s: someone else's file was not quarantined", name)
		}
	}
}

// A file of another size at the name reserved earlier is not this one: it
// stays, and the download takes another name.
func TestOtherFileAtTheReservedNameIsLeftAlone(t *testing.T) {
	e := newEnv(t, 100)
	ctx := context.Background()
	if ok, err := e.st.ReserveMediaPath(ctx, "document:555", finalRel, media.PathKey(finalRel)); !ok || err != nil {
		t.Fatalf("reserve: %v %v", ok, err)
	}
	if err := os.MkdirAll(filepath.Dir(e.final()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.final(), []byte("not this one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := e.d.Run(ctx); err != nil || r.Done != 1 {
		t.Fatalf("report = %+v %v", r, err)
	}
	if b, _ := os.ReadFile(e.final()); string(b) != "not this one" {
		t.Fatal("the other file was overwritten")
	}
	i := e.info(t)
	if got, _ := os.ReadFile(filepath.Join(e.dir, i.LocalPath)); i.LocalPath == finalRel || !bytes.Equal(got, e.data) {
		t.Fatalf("downloaded to %s", i.LocalPath)
	}
}

// quarantinedWhenSent downloads docMessage's file changed by sent, for the
// account with id 42.
func quarantinedWhenSent(t *testing.T, sent func(*tg.Message)) bool {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("quarantine is a macOS attribute")
	}
	e := newEnv(t, 100)
	e.d.SelfID = 42
	m := docMessage(7, 555, "old", 100)
	sent(m)
	raw, err := convert.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateMessageRaw(context.Background(), chatID, 7, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = unix.Getxattr(e.final(), "com.apple.quarantine", make([]byte, 128))
	return err == nil
}

// A file already on disk under the name, even differing only in case, is
// never overwritten: the download takes the next name.
func TestNameTakenOnDiskGetsTheNextOne(t *testing.T) {
	e := newEnv(t, 100)
	other := filepath.Join(e.dir, "media", "1970", "file", "1970-01-01_000001_ОТЧЁТ 2026.pdf")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := e.d.Run(context.Background()); err != nil || r.Done != 1 {
		t.Fatalf("report = %+v %v", r, err)
	}
	if b, _ := os.ReadFile(other); string(b) != "someone else's" {
		t.Fatal("an existing file was overwritten")
	}
	want := filepath.Join("media", "1970", "file", "1970-01-01_000001_отчёт 2026 (2).pdf")
	if i := e.info(t); i.LocalPath != want {
		t.Fatalf("path = %s, want %s", i.LocalPath, want)
	}
}

func TestResumesFromAlignedPartFile(t *testing.T) {
	e := newEnv(t, media.ChunkSize*3)
	if err := os.MkdirAll(filepath.Dir(e.final()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(media.PartPath(e.final()), e.data[:media.ChunkSize+1000], 0o644); err != nil {
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

func TestHangingChunkFailsTheAttempt(t *testing.T) {
	old := media.ChunkTimeout
	media.ChunkTimeout = 50 * time.Millisecond
	defer func() { media.ChunkTimeout = old }()
	e := newEnv(t, 1000)
	e.tg.hang = true
	r, err := e.d.Run(context.Background())
	if err != nil || r.Retry != 1 || r.Failed != 0 || e.info(t).Attempts != 1 {
		t.Fatalf("report = %+v info = %+v err = %v: a hung request must end and count as an attempt", r, e.info(t), err)
	}
}

// A file with attempts left is retried next run and does not fail this one;
// the last attempt gives up and does.
func TestFailureWithAttemptsLeftIsARetry(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.fail = tgerr.New(500, "RPC_CALL_FAIL")
	for i := 1; i <= media.MaxAttempts; i++ {
		r, err := e.d.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		last := i == media.MaxAttempts
		if last && (r.Failed != 1 || r.Retry != 0) || !last && (r.Retry != 1 || r.Failed != 0 || len(r.RetryErrors) != 1) {
			t.Fatalf("run %d: %+v", i, r)
		}
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
	st, err := os.Stat(media.PartPath(e.final()))
	if err != nil || st.Size() != media.ChunkSize {
		t.Fatalf("part = %v %v", st, err)
	}
}

func TestRelPath(t *testing.T) {
	const date = 1718388153 // 2024-06-14 18:02:33 UTC
	for _, c := range []struct {
		job  store.MediaJob
		n    int
		want string
	}{
		{store.MediaJob{Kind: "document", FileName: "Договор аренды.PDF", Mime: "application/pdf"}, 1, "file/2024-06-14_180233_Договор аренды.PDF"},
		{store.MediaJob{Kind: "document", FileName: "Договор аренды.pdf"}, 2, "file/2024-06-14_180233_Договор аренды (2).pdf"},
		{store.MediaJob{Kind: "document", FileName: "../../etc/passwd", Mime: "application/pdf"}, 1, "file/2024-06-14_180233_etcpasswd.pdf"},
		{store.MediaJob{Kind: "photo", ChatTitle: "Семья / дом"}, 1, "photo/2024-06-14_180233_Семья дом.jpg"},
		{store.MediaJob{Kind: "voice", ChatTitle: "Семья", Sender: "Анна"}, 1, "voice/2024-06-14_180233_Семья_Анна.ogg"},
		{store.MediaJob{Kind: "voice", ChatTitle: "Анна", Sender: "Анна"}, 1, "voice/2024-06-14_180233_Анна.ogg"},
		{store.MediaJob{Kind: "voice", Sender: "Анна"}, 1, "voice/2024-06-14_180233_Анна.ogg"},
		{store.MediaJob{Kind: "photo", ChatTitle: "Семья", Sender: "Анна"}, 1, "photo/2024-06-14_180233_Семья.jpg"},
		{store.MediaJob{Kind: "video_note"}, 1, "round/2024-06-14_180233_round.mp4"},
		{store.MediaJob{Kind: "document", FileName: "install.command"}, 1, "file/2024-06-14_180233_install.command.txt"},
		{store.MediaJob{Kind: "document", FileName: "run.SH", ChatTitle: "x"}, 1, "file/2024-06-14_180233_run.SH.txt"},
		{store.MediaJob{Kind: "document", FileName: "invoice.HTML", ChatTitle: "x"}, 1, "file/2024-06-14_180233_invoice.HTML.txt"},
		{store.MediaJob{Kind: "document", FileName: "logo.svg", ChatTitle: "x"}, 1, "file/2024-06-14_180233_logo.svg.txt"},
	} {
		c.job.Date = date
		got := media.RelPath(c.job, time.UTC, c.n)
		if want := filepath.Join("media", "2024", filepath.FromSlash(c.want)); got != want {
			t.Errorf("%+v → %s, want %s", c.job, got, want)
		}
	}
	long := media.RelPath(store.MediaJob{Date: date, Kind: "document", FileName: strings.Repeat("я", 200) + ".pdf"}, time.UTC, 999)
	if n := len(filepath.Base(media.PartPath(long))); n > 255 || !strings.HasSuffix(long, " (999).pdf") {
		t.Errorf("long name: its download is %d bytes: %s", n, long)
	}
	if media.PathKey("media/2024/Фото.JPG") != media.PathKey("media/2024/фото.jpg") ||
		media.PathKey("media/2024/\u0439.jpg") != media.PathKey("media/2024/\u0438\u0306.jpg") ||
		media.PathKey("media/2024/Straße.pdf") != media.PathKey("media/2024/STRASSE.pdf") ||
		media.PathKey("media/2024/\ufb01le.pdf") != media.PathKey("media/2024/file.pdf") {
		t.Error("names APFS sees as one get different keys")
	}
	if e := media.Ext("x.tar.GZ", "", "document"); e != ".GZ" {
		t.Errorf("ext = %s", e)
	}
	if e := media.Ext("evil.p;h p", "", "voice"); e != ".ogg" {
		t.Errorf("unsafe ext accepted: %s", e)
	}
}

func TestReportsProgressPerFile(t *testing.T) {
	e := newEnv(t, 100)
	var calls [][2]int
	e.d.Progress = func(done, total int) { calls = append(calls, [2]int{done, total}) }
	if _, err := e.d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != [2]int{1, 1} {
		t.Fatalf("progress calls = %v", calls)
	}
}
