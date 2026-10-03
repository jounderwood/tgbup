package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/browse"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// rebuildFiles regenerates files/, the readable view of downloaded media.
func rebuildFiles(ctx context.Context, db *store.Store, archiveDir string) (browse.Result, error) {
	files, err := db.DownloadedFiles(ctx)
	if err != nil {
		return browse.Result{}, err
	}
	return browse.Build(archiveDir, files, time.Local)
}

// cmdFiles rebuilds files/ from the archive alone, without Telegram.
func cmdFiles(ctx context.Context, env Env) int {
	log := newLogger(env.Stderr)
	st, err := loadLocalSetup(env, true)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup files:", err)
		return 1
	}
	release, err := acquireLock(st.paths.Lock())
	if errors.Is(err, errLocked) {
		log.Info("skipping: a sync is in progress and rebuilds files/ itself")
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
	res, err := rebuildFiles(ctx, db, st.cfg.ArchiveDir)
	if err != nil {
		log.Error("build files view", "err", err)
		return 1
	}
	printFiles(env.Stdout, res, filepath.Join(st.cfg.ArchiveDir, browse.Dir))
	return 0
}
