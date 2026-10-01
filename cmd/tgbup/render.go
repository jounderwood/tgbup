package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/render"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

func renderArchive(ctx context.Context, db *store.Store, archiveDir string, full bool) (render.Result, error) {
	return render.Render(ctx, db, render.Options{OutDir: archiveDir, Track: true, Full: full, Loc: time.Local})
}

func printRender(w io.Writer, r render.Result, err error) {
	fmt.Fprintf(w, "rendered=%d unchanged=%d\n", r.Pages, r.Skipped)
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
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	st, err := loadSetup(env, true)
	if err != nil {
		log.Error("setup failed", "err", err)
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
