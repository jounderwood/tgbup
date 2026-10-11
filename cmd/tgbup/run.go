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

	"github.com/jounderwood/tgbup/internal/backup"
	"github.com/jounderwood/tgbup/internal/notify"
	"github.com/jounderwood/tgbup/internal/render"
	"github.com/jounderwood/tgbup/internal/store"
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
	warnings     *warnLines // nil in tests that do not look at them
	// progress: tell the bot, quietly, that the run started and finished;
	// trigger and takeout say what started it.
	progress bool
	trigger  string
	takeout  bool
	home     string
}

type runRecord struct {
	NewMessages  int      `json:"new_messages"`
	Edited       int      `json:"edited,omitempty"`
	Deleted      int      `json:"deleted,omitempty"`
	NewMedia     int      `json:"new_media"`
	SyncErrors   []string `json:"sync_errors,omitempty"`
	BackupErrors []string `json:"backup_errors,omitempty"`
	Snapshots    []string `json:"snapshots,omitempty"`
	// Update is the archive's update after the run; Unchanged says no place
	// needed a snapshot of it, it already had one.
	Update    int  `json:"update,omitempty"`
	Unchanged bool `json:"unchanged,omitempty"`
	// Log is the run's warnings and errors as logged, for the web page.
	Log []string `json:"log,omitempty"`
}

const summaryKey = "notify:summary_at"

// runOnce syncs, backs up whatever is on disk even after a failed sync,
// records the run, and tells the bot about errors and, once per interval, sends a summary.
func runOnce(ctx context.Context, p runParts) int {
	started := p.now()
	p.quiet(ctx, notify.StartText(p.host, p.trigger, p.takeout))
	sum, err := p.sync(ctx)
	sum.print(p.out)
	rec := runRecord{NewMessages: sum.sync.NewMessages, Edited: sum.sync.Edited, Deleted: sum.sync.Deleted, NewMedia: sum.media.Done,
		SyncErrors: sum.problems(err)}

	rep, configured := p.backup(ctx)
	switch {
	case !configured:
		fmt.Fprintln(p.out, "backup: off, the archive is kept as files only")
	case rep.Integrity != nil:
		rec.BackupErrors = append(rec.BackupErrors, rep.Integrity.Error())
	}
	if u, ok, err := p.db.LastUpdate(ctx); err == nil && ok {
		rec.Update = u.N
	}
	made := false
	rec.Unchanged = len(rep.Repos) > 0
	for _, r := range rep.Repos {
		if r.Err != nil {
			rec.BackupErrors = append(rec.BackupErrors, r.Err.Error())
			rec.Unchanged = false
			continue
		}
		if r.Warning != "" {
			rec.BackupErrors = append(rec.BackupErrors, backupWarning(r))
		}
		if !r.Skipped {
			rec.Snapshots = append(rec.Snapshots, backup.Display(r.Repo)+": "+r.Snapshot)
			rec.Unchanged, made = false, true
		}
	}
	printBackup(p.out, rep)
	if made {
		// history.html names the snapshots just made.
		if err := render.WriteHistory(ctx, p.db, archiveRender(p.archiveDir)); err != nil {
			p.log.Error("write the update history", "err", err)
		}
	}

	ok := len(rec.SyncErrors) == 0 && len(rec.BackupErrors) == 0
	if p.warnings != nil {
		rec.Log = p.warnings.lines()
	}
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
		if p.bot.Token != "" || p.bot.ChatID != "" {
			fmt.Fprintln(p.out, "alerts: off: BOT_TOKEN and BOT_CHAT_ID must both be set (tgbup init sets them)")
		} else {
			fmt.Fprintln(p.out, "alerts: no bot set up (tgbup init)")
		}
		return code
	}
	if !ok {
		if err := p.bot.Send(ctx, notify.ErrorText(p.host, notify.RunResult{SyncErrors: rec.SyncErrors, BackupErrors: rec.BackupErrors})); err != nil {
			p.log.Error("notify", "err", err)
			code = 1
		}
	} else {
		var places, same []string
		for _, r := range rep.Repos {
			switch {
			case r.Err != nil:
			case r.Skipped:
				same = append(same, placeName(r.Repo, p.home))
			default:
				places = append(places, placeName(r.Repo, p.home)+" "+shortID(r.Snapshot))
			}
		}
		p.quiet(ctx, notify.DoneText(p.host, notify.Counts{New: rec.NewMessages, Edited: rec.Edited, Deleted: rec.Deleted, Files: rec.NewMedia},
			places, same))
	}
	if err := p.maybeSummary(ctx); err != nil {
		p.log.Error("notify summary", "err", err)
		code = 1
	}
	return code
}

// maybeSummary sends the periodic summary when the interval has passed; the
// first run only starts the interval.
// quiet sends routine news when the person wants it; a failure to send it is
// only logged: it must not fail the run.
func (p runParts) quiet(ctx context.Context, text string) {
	if !p.progress || !p.bot.Enabled() {
		return
	}
	if err := p.bot.SendQuiet(ctx, text); err != nil {
		p.log.Warn("notify", "err", err)
	}
}

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
		s.LastBackup = append(s.LastBackup, notify.RepoTime{Repo: backup.Label(repo, p.home), At: at})
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
		if rr.Err != nil {
			fmt.Fprintf(w, "backup → %s: failed: %v\n", backup.Display(rr.Repo), rr.Err)
			continue
		}
		extra := ""
		if rr.Pruned {
			extra += " · old snapshots pruned"
		}
		if rr.Checked {
			extra += " · repository checked"
		}
		if rr.Skipped {
			fmt.Fprintf(w, "backup → %s: nothing changed since snapshot %s, no new one%s\n", backup.Display(rr.Repo), shortID(rr.Snapshot), extra)
			continue
		}
		fmt.Fprintf(w, "backup → %s: snapshot %s%s\n", backup.Display(rr.Repo), shortID(rr.Snapshot), extra)
		if rr.Warning != "" {
			fmt.Fprintf(w, "  warning: %s\n", backupWarning(rr))
		}
	}
}

// backupWarning reports a snapshot that misses files restic could not read
// (on macOS often a denied folder access): the backup is not complete.
func backupWarning(r backup.RepoResult) string {
	return backup.Display(r.Repo) + ": snapshot made, but some files could not be read: " + r.Warning
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
