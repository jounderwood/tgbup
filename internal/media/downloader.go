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

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbp/internal/convert"
	"gitlab.com/joeunderwood/tgbp/internal/store"
	"gitlab.com/joeunderwood/tgbp/internal/tgclient"
)

// upload.getFile needs offset and limit to be multiples of 4 KiB with 1 MiB divisible
// by limit; 512 KiB aligned offsets never cross a 1 MiB boundary.
const ChunkSize = 512 * 1024

const MaxAttempts = 5

type Downloader struct {
	TG          tgclient.Telegram
	Store       *store.Store
	ArchiveDir  string
	Peers       map[int64]tg.InputPeerClass
	Workers     int
	MaxFileSize int64
	SkipKinds   []string
	Log         *slog.Logger
}

type Report struct {
	Done     int
	Skipped  int
	Failed   int
	Deferred int
	Errors   []error
	AuthErr  error
}

type outcome int

const (
	outDone outcome = iota
	outSkipped
	outDeferred
)

func (d *Downloader) Run(ctx context.Context) (Report, error) {
	jobs, err := d.Store.PendingMedia(ctx)
	if err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		mu sync.Mutex
		r  Report
		wg sync.WaitGroup
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
				countsAsAttempt := err != nil &&
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
				case err == nil, kind == tgclient.KindFlood, kind == tgclient.KindCanceled:
					r.Deferred++
				case kind == tgclient.KindAuth:
					r.AuthErr = err
					cancel(err)
				default:
					r.Failed++
					r.Errors = append(r.Errors, fmt.Errorf("media %s: %w", j.Key, err))
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

func (d *Downloader) handle(ctx context.Context, j store.MediaJob) (outcome, error) {
	if slices.Contains(d.SkipKinds, j.Kind) {
		return outSkipped, d.Store.MediaSkipped(ctx, j.Key, "kind "+j.Kind+" is skipped by config")
	}
	if d.MaxFileSize > 0 && j.Size > d.MaxFileSize {
		return outSkipped, d.Store.MediaSkipped(ctx, j.Key, fmt.Sprintf("size %d exceeds max_file_size %d", j.Size, d.MaxFileSize))
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
		chunk, err := t.FileChunk(ctx, loc, offset, ChunkSize)
		if err != nil {
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
