package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// runParts are the pieces of one scheduled run, swappable in tests.
type runParts struct {
	sync         func(context.Context) (syncSummary, error)
	backup       func(context.Context) (backup.Report, bool) // false: backup is not configured
	db           *store.Store
	bot          notify.Bot
	host         string
	archiveDir   string
	repos        []string
	summaryEvery time.Duration
	now          func() time.Time
	loc          *time.Location
	out          io.Writer
	log          *slog.Logger
}

type runRecord struct {
	NewMessages  int      `json:"new_messages"`
	NewMedia     int      `json:"new_media"`
	SyncErrors   []string `json:"sync_errors,omitempty"`
	BackupErrors []string `json:"backup_errors,omitempty"`
	Snapshots    []string `json:"snapshots,omitempty"`
}

const summaryKey = "notify:summary_at"

// runOnce syncs, backs up whatever is on disk even after a failed sync,
// records the run, and tells the bot about errors and, once per interval, sends a summary.
func runOnce(ctx context.Context, p runParts) int {
	started := p.now()
	sum, err := p.sync(ctx)
	sum.print(p.out)
	rec := runRecord{NewMessages: sum.sync.NewMessages, NewMedia: sum.media.Done, SyncErrors: sum.problems(err)}

	rep, configured := p.backup(ctx)
	switch {
	case !configured:
		fmt.Fprintln(p.out, "backup: not configured")
	case rep.Integrity != nil:
		rec.BackupErrors = append(rec.BackupErrors, rep.Integrity.Error())
	}
	for _, r := range rep.Repos {
		if r.Err != nil {
			rec.BackupErrors = append(rec.BackupErrors, r.Err.Error())
			continue
		}
		rec.Snapshots = append(rec.Snapshots, r.Repo+": "+r.Snapshot)
	}
	printBackup(p.out, rep)

	ok := len(rec.SyncErrors) == 0 && len(rec.BackupErrors) == 0
	summary, _ := json.Marshal(rec)
	code := 0
	if !ok {
		code = 1
	}
	if err := p.db.AddRun(ctx, store.Run{Started: started, Finished: p.now(), OK: ok, Summary: string(summary)}); err != nil {
		p.log.Error("record the run", "err", err)
		code = 1
	}
	if !p.bot.Enabled() {
		fmt.Fprintln(p.out, "notify: not configured")
		return code
	}
	if !ok {
		if err := p.bot.Send(ctx, notify.ErrorText(p.host, notify.RunResult{SyncErrors: rec.SyncErrors, BackupErrors: rec.BackupErrors})); err != nil {
			p.log.Error("notify", "err", err)
			code = 1
		}
	}
	if err := p.maybeSummary(ctx); err != nil {
		p.log.Error("notify summary", "err", err)
		code = 1
	}
	return code
}

// maybeSummary sends the periodic summary when the interval has passed; the
// first run only starts the interval.
func (p runParts) maybeSummary(ctx context.Context) error {
	now := p.now()
	v, found, err := p.db.Meta(ctx, summaryKey)
	if err != nil {
		return err
	}
	last := now
	if found {
		n, _ := strconv.ParseInt(v, 10, 64)
		last = time.Unix(n, 0)
	}
	if found && now.Sub(last) < p.summaryEvery {
		return nil
	}
	if found {
		s, err := p.summary(ctx, last)
		if err != nil {
			return err
		}
		if err := p.bot.Send(ctx, notify.SummaryText(p.host, s, p.loc)); err != nil {
			return err
		}
	}
	return p.db.SetMeta(ctx, summaryKey, strconv.FormatInt(now.Unix(), 10))
}

func (p runParts) summary(ctx context.Context, since time.Time) (notify.Summary, error) {
	s := notify.Summary{Since: since}
	runs, err := p.db.RunsSince(ctx, since)
	if err != nil {
		return s, err
	}
	for _, r := range runs {
		var rec runRecord
		_ = json.Unmarshal([]byte(r.Summary), &rec)
		s.Runs++
		if !r.OK {
			s.FailedRuns++
		}
		s.NewMessages += rec.NewMessages
		s.NewMedia += rec.NewMedia
	}
	if s.GaveUpFiles, err = p.db.CountMedia(ctx, "failed"); err != nil {
		return s, err
	}
	s.ArchiveBytes = archiveSize(p.archiveDir)
	for _, repo := range p.repos {
		at, _ := backup.LastSuccess(ctx, p.db, repo)
		s.LastBackup = append(s.LastBackup, notify.RepoTime{Repo: repo, At: at})
	}
	return s, nil
}

// archiveSize counts the database and media/, not the views rebuilt from them.
func archiveSize(dir string) int64 {
	var n int64
	for _, name := range []string{"archive.sqlite", "archive.sqlite-wal"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
			n += st.Size()
		}
	}
	_ = filepath.WalkDir(filepath.Join(dir, "media"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func printBackup(w io.Writer, r backup.Report) {
	if r.Integrity != nil {
		fmt.Fprintf(w, "backup: %v\n", r.Integrity)
	}
	for _, rr := range r.Repos {
		switch {
		case rr.Err != nil:
			fmt.Fprintf(w, "backup %s: %v\n", rr.Repo, rr.Err)
		default:
			fmt.Fprintf(w, "backup %s: snapshot %s pruned=%v checked=%v\n", rr.Repo, shortID(rr.Snapshot), rr.Pruned, rr.Checked)
		}
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
