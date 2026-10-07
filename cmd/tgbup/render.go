package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/render"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// renderArchive redraws the archive's pages and, when anything changed,
// records it as a new update (see store.NoteUpdate), even when drawing failed:
// the new messages and files are in the archive all the same, and the backup
// must take them.
func renderArchive(ctx context.Context, db *store.Store, archiveDir string, full bool) (render.Result, error) {
	opts := archiveRender(archiveDir)
	opts.Full = full
	var res render.Result
	err := applyTodoEvents(ctx, db)
	if err != nil {
		err = fmt.Errorf("update checklists: %w", err)
	} else {
		res, err = render.Render(ctx, db, opts)
	}
	pages := res.Pages
	if full || err != nil {
		pages = 0 // redrawn on purpose, or not all: not a sign of change
	}
	_, created, _, uerr := db.NoteUpdate(ctx, pages, time.Now())
	switch {
	case uerr != nil:
		return res, errors.Join(err, fmt.Errorf("record the update: %w", uerr))
	case created && err == nil:
		err = render.WriteHistory(ctx, db, opts)
	}
	return res, err
}

func archiveRender(archiveDir string) render.Options {
	return render.Options{OutDir: archiveDir, Track: true, Loc: time.Local}
}

// applyTodoEvents brings every archived checklist up to the ticks and items
// added after it was downloaded: Telegram sends those as separate events and
// does not resend the checklist. A changed checklist changes its page, which
// is then redrawn.
func applyTodoEvents(ctx context.Context, db *store.Store) error {
	events, err := db.TodoEvents(ctx)
	if err != nil {
		return err
	}
	type key struct {
		chat int64
		id   int
	}
	metas, changed := map[key]string{}, map[key]bool{}
	for _, e := range events {
		k := key{e.ChatID, e.Checklist}
		meta, seen := metas[k]
		if !seen {
			if meta, err = db.MessageMeta(ctx, e.ChatID, e.Checklist); err != nil {
				return err
			}
		}
		if next, ok := convert.ApplyTodoEvent(meta, e.ActionJSON, e.FromID, int(e.Date)); ok {
			meta, changed[k] = next, true
		}
		metas[k] = meta
	}
	for k := range changed {
		if err := db.SetMessageMeta(ctx, k.chat, k.id, metas[k]); err != nil {
			return err
		}
	}
	return nil
}

func printRender(w io.Writer, r render.Result, err error) {
	fmt.Fprintf(w, "pages: %s redrawn · %s unchanged\n", groupDigits(r.Pages), groupDigits(r.Skipped))
	for _, e := range r.Errors {
		fmt.Fprintf(w, "render: %v\n", e)
	}
	if err != nil {
		fmt.Fprintf(w, "render: %v\n", err)
	}
}

// cmdRender redraws html/ and md/ from the archive alone, without Telegram.
func cmdRender(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	full := fs.Bool("full", false, "redraw every page, not only the changed ones")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := newLogger(env.Stderr)
	st, err := loadLocalSetup(env, true)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup render:", err)
		return 1
	}
	release, err := acquireLock(st.paths.Lock())
	if errors.Is(err, errLocked) {
		log.Info("skipping: a sync is in progress and renders the archive itself")
		return 0
	}
	if err != nil {
		log.Error("lock failed", "err", err)
		return 1
	}
	defer release()
	db, err := store.Open(filepath.Join(st.cfg.ArchiveDir, "archive.sqlite"))
	if err != nil {
		log.Error("open archive", "err", err)
		return 1
	}
	defer db.Close()
	res, err := renderArchive(ctx, db, st.cfg.ArchiveDir, *full)
	printRender(env.Stdout, res, err)
	if err != nil || len(res.Errors) > 0 {
		return 1
	}
	return 0
}
