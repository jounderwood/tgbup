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

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
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
	Log         *slog.Logger
	// Progress, if set, is called after each file with how many of the
	// pending files have been handled.
	Progress func(done, total int)
}

type Report struct {
	Done     int
	Skipped  int
	Failed   int
	Deferred int
	Errors   []error
	AuthErr  error
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
				if countsAsAttempt {
					if ferr := d.Store.MediaAttemptFailed(context.WithoutCancel(ctx), j.Key, err.Error(), MaxAttempts); ferr != nil {
						err = errors.Join(err, ferr)
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
	rel := RelPath(j)
	final := filepath.Join(d.ArchiveDir, rel)
	if _, err := os.Stat(final); err == nil {
		return outDone, d.Store.MediaDone(ctx, j.Key, rel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if err := d.fetch(ctx, j, peer, final); err != nil {
		return 0, err
	}
	return outDone, d.Store.MediaDone(ctx, j.Key, rel)
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
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	part := final + ".part"
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
	return os.Rename(part, final)
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
