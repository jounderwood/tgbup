package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

// upload.getFile needs offset and limit to be multiples of 4 KiB with 1 MiB divisible
// by limit; 512 KiB aligned offsets never cross a 1 MiB boundary.
const ChunkSize = 512 * 1024

const MaxAttempts = 5

// ChunkTimeout bounds one chunk request. gotd waits without a limit for a
// connection to the data center holding a file, and one that never comes
// would hang the run and, through run.lock, every scheduled run after it.
var ChunkTimeout = 2 * time.Minute

type Downloader struct {
	TG          tgclient.Telegram
	Store       *store.Store
	ArchiveDir  string
	Peers       map[int64]tg.InputPeerClass
	Workers     int
	MaxFileSize int64
	SkipKinds   []string
	// SelfID is the account's own user id: its files are not quarantined.
	SelfID int64
	// Location is the time zone of the dates in file names; nil is the Mac's.
	Location *time.Location
	Log      *slog.Logger
	// Progress, if set, is called after each file with how many of the
	// pending files have been handled.
	Progress func(done, total int)
}

type Report struct {
	Done     int
	Skipped  int
	Failed   int // gave up after MaxAttempts
	Deferred int
	Errors   []error
	// Retry counts files that failed this time but have attempts left: they
	// are tried again next run, so the run is not failed for them.
	Retry       int
	RetryErrors []error
	AuthErr     error
	// LocalErr is a disk-side failure (full disk, permissions); it stops the
	// media phase and is not held against the files.
	LocalErr error
}

type outcome int

const (
	outDone outcome = iota
	outSkipped
	outDeferred
)

func (d *Downloader) Run(ctx context.Context) (Report, error) {
	if err := d.requeueSkipped(ctx); err != nil {
		return Report{}, err
	}
	jobs, err := d.Store.PendingMedia(ctx)
	if err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		mu      sync.Mutex
		r       Report
		wg      sync.WaitGroup
		handled int
	)
	ch := make(chan store.MediaJob)
	for range max(d.Workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				out, err := d.handle(ctx, j)
				kind := tgclient.KindOther
				if err != nil {
					kind = tgclient.Classify(err)
				}
				local := err != nil && isLocal(err)
				countsAsAttempt := err != nil && !local &&
					(kind == tgclient.KindOther || kind == tgclient.KindFileReference || kind == tgclient.KindInaccessible)
				gaveUp := false
				if countsAsAttempt {
					var ferr error
					if gaveUp, ferr = d.Store.MediaAttemptFailed(context.WithoutCancel(ctx), j.Key, err.Error(), MaxAttempts); ferr != nil {
						err, gaveUp = errors.Join(err, ferr), true
					}
				}
				mu.Lock()
				switch {
				case err == nil && out == outDone:
					r.Done++
				case err == nil && out == outSkipped:
					r.Skipped++
				case local:
					if r.LocalErr == nil {
						r.LocalErr = fmt.Errorf("media %s: %w", j.Key, err)
					}
					cancel(err)
				case err == nil, kind == tgclient.KindFlood, kind == tgclient.KindCanceled:
					r.Deferred++
				case kind == tgclient.KindAuth:
					r.AuthErr = err
					cancel(err)
				case !gaveUp:
					r.Retry++
					r.RetryErrors = append(r.RetryErrors, fmt.Errorf("media %s: %w", j.Key, err))
				default:
					r.Failed++
					r.Errors = append(r.Errors, fmt.Errorf("media %s: %w", j.Key, err))
				}
				handled++
				if d.Progress != nil {
					d.Progress(handled, len(jobs))
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(ch)
	wg.Wait()
	return r, nil
}

func isLocal(err error) bool {
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	return errors.As(err, &pathErr) || errors.As(err, &linkErr)
}

func (d *Downloader) skipReason(kind string, size int64) string {
	if slices.Contains(d.SkipKinds, kind) {
		return "kind " + kind + " is skipped by config"
	}
	if d.MaxFileSize > 0 && size > d.MaxFileSize {
		return fmt.Sprintf("size %d exceeds max_file_size %d", size, d.MaxFileSize)
	}
	return ""
}

// requeueSkipped makes a config change (higher limit, fewer skipped kinds)
// apply to files skipped by earlier runs.
func (d *Downloader) requeueSkipped(ctx context.Context) error {
	skipped, err := d.Store.SkippedMedia(ctx)
	if err != nil {
		return err
	}
	for _, m := range skipped {
		if d.skipReason(m.Kind, m.Size) == "" {
			if err := d.Store.RequeueMedia(ctx, m.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Downloader) handle(ctx context.Context, j store.MediaJob) (outcome, error) {
	if reason := d.skipReason(j.Kind, j.Size); reason != "" {
		return outSkipped, d.Store.MediaSkipped(ctx, j.Key, reason)
	}
	peer, ok := d.Peers[j.ChatID]
	if !ok {
		return outDeferred, nil
	}
	rel := j.LocalPath
	if rel == "" {
		var err error
		if rel, err = d.reserve(ctx, j); err != nil {
			return 0, err
		}
	}
	final := filepath.Join(d.ArchiveDir, rel)
	// The name was free when it was reserved, so a file there of the right
	// size is this one, finished by a run that stopped before recording it.
	// Any other file there is not ours: leave it and take another name.
	if st, err := os.Lstat(final); err == nil {
		if j.Size <= 0 || st.Size() == j.Size {
			return outDone, d.Store.MediaDone(ctx, j.Key, rel)
		}
		if err := d.Store.ReleaseMediaPath(ctx, j.Key); err != nil {
			return 0, err
		}
		if rel, err = d.reserve(ctx, j); err != nil {
			return 0, err
		}
		final = filepath.Join(d.ArchiveDir, rel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if err := d.fetch(ctx, j, peer, final); err != nil {
		return 0, err
	}
	return outDone, d.Store.MediaDone(ctx, j.Key, rel)
}

// maxSameName bounds the " (n)" candidates tried for one name.
const maxSameName = 1000

// reserve picks the file's name: the first candidate that no other file in
// the archive has and nothing on disk uses. It is recorded before the
// download, so a later attempt continues into the same file.
func (d *Downloader) reserve(ctx context.Context, j store.MediaJob) (string, error) {
	loc := d.Location
	if loc == nil {
		loc = time.Local
	}
	for n := 1; n <= maxSameName; n++ {
		rel := RelPath(j, loc, n)
		if _, err := os.Lstat(filepath.Join(d.ArchiveDir, rel)); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		ok, err := d.Store.ReserveMediaPath(ctx, j.Key, rel, PathKey(rel))
		if err != nil {
			return "", err
		}
		if ok {
			return rel, nil
		}
	}
	return "", fmt.Errorf("no free name for %s after %d tries", RelPath(j, loc, 1), maxSameName)
}

func (d *Downloader) fetch(ctx context.Context, j store.MediaJob, peer tg.InputPeerClass, final string) error {
	raw, err := d.Store.MessageRaw(ctx, j.ChatID, j.MessageID)
	if err != nil {
		return err
	}
	msg, err := tg.DecodeMessage(&bin.Buffer{Buf: raw})
	if err != nil {
		return fmt.Errorf("decode stored message: %w", err)
	}
	loc, err := convert.FileLocation(msg)
	if err != nil {
		return err
	}
	foreign := !ownFile(msg, d.SelfID)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	part := PartPath(final)
	for refreshed := false; ; refreshed = true {
		err := download(ctx, d.TG, loc, part)
		if err == nil {
			break
		}
		if refreshed || tgclient.Classify(err) != tgclient.KindFileReference {
			return err
		}
		fresh, err := d.TG.Message(ctx, peer, j.MessageID)
		if err != nil {
			return fmt.Errorf("refresh file reference: %w", err)
		}
		raw, err := convert.Encode(fresh)
		if err != nil {
			return err
		}
		if err := d.Store.UpdateMessageRaw(ctx, j.ChatID, j.MessageID, raw); err != nil {
			return err
		}
		if loc, err = convert.FileLocation(fresh); err != nil {
			return err
		}
	}
	if j.Size > 0 {
		st, err := os.Stat(part)
		if err != nil {
			return err
		}
		if st.Size() != j.Size {
			os.Remove(part)
			return fmt.Errorf("size mismatch: got %d bytes, want %d", st.Size(), j.Size)
		}
	}
	if foreign {
		// Only a warning: a volume without extended attributes (some network
		// shares) must not stop the archive.
		if err := quarantine(part, time.Now()); err != nil {
			d.Log.Warn("could not mark the file as downloaded", "file", final, "err", err)
		}
	}
	date := time.Unix(j.Date, 0)
	if err := os.Chtimes(part, date, date); err != nil {
		return err
	}
	// Names are reserved, so something at final is not ours: never overwrite it.
	if _, err := os.Lstat(final); err == nil {
		return &fs.PathError{Op: "rename", Path: final, Err: fs.ErrExist}
	}
	return os.Rename(part, final)
}

// ownFile tells a file the account's owner sent from one someone else did:
// a forward of someone else's file is theirs. Telegram does not always mark
// the owner's messages in Saved Messages as outgoing, so the sender counts
// too, and a message there without one is the owner's.
func ownFile(msg tg.MessageClass, self int64) bool {
	m, ok := msg.(*tg.Message)
	if !ok {
		return false
	}
	if _, forwarded := m.GetFwdFrom(); forwarded {
		return false
	}
	if _, viaBot := m.GetViaBotID(); viaBot {
		return false // an inline bot made the content
	}
	if m.Out {
		return true
	}
	if from, ok := m.GetFromID(); ok {
		u, isUser := from.(*tg.PeerUser)
		return isUser && self != 0 && u.UserID == self
	}
	u, isUser := m.PeerID.(*tg.PeerUser)
	return isUser && self != 0 && u.UserID == self
}

func download(ctx context.Context, t tgclient.Telegram, loc tg.InputFileLocationClass, part string) error {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	offset := st.Size() - st.Size()%ChunkSize
	if err := f.Truncate(offset); err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	for {
		cctx, cancel := context.WithTimeout(ctx, ChunkTimeout)
		chunk, err := t.FileChunk(cctx, loc, offset, ChunkSize)
		cancel()
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				// Not a cancellation: an attempt that counts, so a file that never
				// answers ends up failed instead of deferred forever.
				return fmt.Errorf("no answer from Telegram for %s", ChunkTimeout)
			}
			return err
		}
		if _, err := f.Write(chunk); err != nil {
			return err
		}
		offset += int64(len(chunk))
		if len(chunk) < ChunkSize {
			return f.Sync()
		}
	}
}
