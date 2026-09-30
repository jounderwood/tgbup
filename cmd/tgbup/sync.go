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

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/media"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/syncer"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type syncSummary struct {
	sync  syncer.Report
	media media.Report
	// unresolved lists configured includes that matched no dialog: such a chat
	// silently stops being archived, so it fails the run.
	unresolved []string
}

func (s syncSummary) ok() bool {
	return s.sync.OK() && len(s.unresolved) == 0 &&
		s.media.AuthErr == nil && s.media.LocalErr == nil && s.media.Failed == 0
}

func (s syncSummary) print(w io.Writer) {
	for _, u := range s.unresolved {
		fmt.Fprintf(w, "config: %s\n", u)
	}
	fmt.Fprintf(w, "chats_synced=%d new_messages=%d deferred=%d newly_inaccessible=%v chat_errors=%d\n",
		s.sync.Chats, s.sync.NewMessages, len(s.sync.Deferred), s.sync.NewlyInaccessible, len(s.sync.Failed))
	for id, err := range s.sync.Failed {
		fmt.Fprintf(w, "  chat %d: %v\n", id, err)
	}
	fmt.Fprintf(w, "media_done=%d media_skipped=%d media_deferred=%d media_failed=%d\n",
		s.media.Done, s.media.Skipped, s.media.Deferred, s.media.Failed)
	for _, err := range s.media.Errors {
		fmt.Fprintf(w, "  %v\n", err)
	}
	if s.media.LocalErr != nil {
		fmt.Fprintf(w, "media stopped by a local disk error: %v\n", s.media.LocalErr)
	}
}

func cmdSync(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	useTakeout := fs.Bool("takeout", false, "use a Telegram takeout session (initial full export)")
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
		log.Info("skipping: another run is in progress")
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

	var sum syncSummary
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		body := func(ctx context.Context, raw *tg.Client) error {
			var err error
			sum, err = runSync(ctx, raw, db, st.cfg, log)
			return err
		}
		if *useTakeout {
			return tgclient.WithTakeout(ctx, c, int64(st.cfg.Media.MaxFileSize), body)
		}
		return body(ctx, c.API())
	})
	sum.print(env.Stdout)
	if err != nil {
		log.Error("sync failed", "err", err)
		return 1
	}
	if !sum.ok() {
		return 1
	}
	return 0
}

func runSync(ctx context.Context, raw *tg.Client, db *store.Store, cfg config.Config, log *slog.Logger) (syncSummary, error) {
	api := tgclient.NewAPI(raw)
	self, err := tgclient.SelfID(ctx, raw)
	if err != nil {
		return syncSummary{}, err
	}
	dialogs, err := api.Dialogs(ctx)
	if err != nil {
		return syncSummary{}, err
	}
	folders, err := api.Folders(ctx, self)
	if err != nil {
		return syncSummary{}, err
	}
	targets, warnings, err := tgclient.SelectTargets(dialogs, folders, cfg.Targets)
	if err != nil {
		return syncSummary{}, err
	}
	targets, migrationWarnings := api.AddMigrated(ctx, targets)
	for _, w := range migrationWarnings {
		log.Warn(w)
	}
	log.Info("targets selected", "count", len(targets))

	dialogIDs := map[int64]bool{}
	for _, d := range dialogs {
		dialogIDs[d.ID] = true
	}
	peers := map[int64]tg.InputPeerClass{}
	for _, t := range targets {
		peers[t.Chat.ID] = t.Peer
	}

	s := syncer.Syncer{
		TG: api, Store: db, SelfID: self, Budget: cfg.Sync.RunTimeBudget.D(), PageSize: 100, Now: time.Now, Log: log,
	}
	sum := syncSummary{sync: s.Run(ctx, targets, dialogIDs), unresolved: warnings}
	if sum.sync.AuthErr != nil {
		return sum, sum.sync.AuthErr
	}

	d := media.Downloader{
		TG: api, Store: db, ArchiveDir: cfg.ArchiveDir, Peers: peers, Workers: cfg.Media.Workers,
		MaxFileSize: int64(cfg.Media.MaxFileSize), SkipKinds: cfg.Media.SkipKinds, Log: log,
	}
	sum.media, err = d.Run(ctx)
	if err != nil {
		return sum, err
	}
	return sum, sum.media.AuthErr
}
