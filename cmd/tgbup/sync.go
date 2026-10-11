package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/config"
	"github.com/jounderwood/tgbup/internal/media"
	"github.com/jounderwood/tgbup/internal/render"
	"github.com/jounderwood/tgbup/internal/stats"
	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/syncer"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

type syncSummary struct {
	sync  syncer.Report
	media media.Report
	// unresolved lists configured includes that matched no dialog: such a chat
	// silently stops being archived, so it fails the run.
	unresolved []string
	// notes are config problems that lose nothing (e.g. a stale exclude_topics key).
	notes     []string
	render    render.Result
	renderErr error
	// viewsDone is set once the pages were redrawn.
	viewsDone bool
}

func (s syncSummary) ok() bool {
	return s.sync.OK() && len(s.unresolved) == 0 && s.renderErr == nil && len(s.render.Errors) == 0 &&
		s.media.AuthErr == nil && s.media.LocalErr == nil && s.media.Failed == 0
}

func (s syncSummary) print(w io.Writer) {
	for _, u := range s.unresolved {
		fmt.Fprintf(w, "config: %s\n", u)
	}
	for _, n := range s.notes {
		fmt.Fprintf(w, "warning: %s\n", n)
	}
	fmt.Fprintf(w, "messages: %s new in %s", groupDigits(s.sync.NewMessages), plural(s.sync.Chats, "chat"))
	if s.sync.Edited > 0 {
		fmt.Fprintf(w, " · %s edited", groupDigits(s.sync.Edited))
	}
	if s.sync.Updated > 0 {
		fmt.Fprintf(w, " · %s with new reactions or votes", groupDigits(s.sync.Updated))
	}
	if n := len(s.sync.Deferred); n > 0 {
		fmt.Fprintf(w, " · %d deferred to the next run", n)
	}
	if n := len(s.sync.Failed); n > 0 {
		fmt.Fprintf(w, " · %d failed", n)
	}
	fmt.Fprintln(w)
	if len(s.sync.NewlyInaccessible) > 0 {
		fmt.Fprintf(w, "  no longer accessible (left or removed): %v\n", s.sync.NewlyInaccessible)
	}
	for id, err := range s.sync.Failed {
		fmt.Fprintf(w, "  chat %d: %v\n", id, err)
	}
	fmt.Fprintf(w, "media: %s downloaded", groupDigits(s.media.Done))
	for _, part := range []struct {
		n    int
		what string
	}{{s.media.Skipped, "skipped by settings"}, {s.media.Deferred, "deferred"}, {s.media.Retry, "to retry next run"}, {s.media.Failed, "failed"}} {
		if part.n > 0 {
			fmt.Fprintf(w, " · %s %s", groupDigits(part.n), part.what)
		}
	}
	fmt.Fprintln(w)
	for _, err := range s.media.Errors {
		fmt.Fprintf(w, "  %v\n", err)
	}
	for _, err := range s.media.RetryErrors {
		fmt.Fprintf(w, "  %v (retried next run)\n", err)
	}
	if s.media.LocalErr != nil {
		fmt.Fprintf(w, "media stopped by a local disk error: %v\n", s.media.LocalErr)
	}
	printRender(w, s.render, s.renderErr)
}

func cmdSync(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	useTakeout := fs.Bool("takeout", false, "use a Telegram takeout session (initial full export)")
	dry := fs.Bool("dry-run", false, "show which chats would be archived and how many messages they hold; download nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dry && *useTakeout {
		fmt.Fprintln(env.Stderr, "--dry-run and --takeout cannot be combined")
		return 2
	}
	log := newLogger(env.Stderr)

	st, err := loadSetup(env, true)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup sync:", err)
		return 1
	}
	if *dry {
		return dryRun(ctx, st, env, log)
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

	sum, err := syncArchive(ctx, st, db, log, env.Stderr, *useTakeout)
	return finishSync(env.Stdout, log, sum, err)
}

// syncArchive runs one sync into an open archive; the caller holds run.lock.
func syncArchive(ctx context.Context, st setup, db *store.Store, log *slog.Logger, progress io.Writer, takeout bool) (syncSummary, error) {
	var sum syncSummary
	err := tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		body := func(ctx context.Context, raw *tg.Client) error {
			var err error
			sum, err = runSync(ctx, raw, db, st.cfg, log, progress)
			return err
		}
		if takeout {
			return tgclient.WithTakeout(ctx, c, int64(st.cfg.Media.MaxFileSize), body)
		}
		return body(ctx, c.API())
	})
	if err != nil && !sum.viewsDone && ctx.Err() == nil {
		// Telegram failed before the pages were redrawn; they need no Telegram.
		sum.render, sum.renderErr = renderArchive(ctx, db, st.cfg.ArchiveDir, false)
	}
	if err != nil && tgclient.Classify(err) == tgclient.KindAuth && !errors.Is(err, tgclient.ErrNotLoggedIn) {
		err = fmt.Errorf("%w: the Telegram session is no longer valid: run tgbup init (or tgbup login)", err)
	}
	return sum, err
}

// problems lists what went wrong in a sync, one line each, for notifications.
func (s syncSummary) problems(err error) []string {
	var out []string
	if err != nil {
		out = append(out, err.Error())
	}
	out = append(out, s.unresolved...)
	// Reported once: a chat is "newly" inaccessible only on the run that finds it so.
	for _, id := range s.sync.NewlyInaccessible {
		out = append(out, fmt.Sprintf("chat %d is no longer accessible (left, removed or banned); its archive stays", id))
	}
	for id, e := range s.sync.Failed {
		out = append(out, fmt.Sprintf("chat %d: %v", id, e))
	}
	for _, e := range s.media.Errors {
		out = append(out, e.Error())
	}
	if s.media.LocalErr != nil {
		out = append(out, "media stopped by a local disk error: "+s.media.LocalErr.Error())
	}
	for _, e := range s.render.Errors {
		out = append(out, "render: "+e.Error())
	}
	if s.renderErr != nil {
		out = append(out, "render: "+s.renderErr.Error())
	}
	return out
}

func finishSync(w io.Writer, log *slog.Logger, sum syncSummary, err error) int {
	var unconfirmed *tgclient.TakeoutNotConfirmed
	if errors.As(err, &unconfirmed) {
		log.Info(unconfirmed.Error())
		return 1
	}
	sum.print(w)
	if err != nil {
		log.Error("sync failed", "err", err.Error())
		return 1
	}
	if !sum.ok() {
		return 1
	}
	return 0
}

type chatSelection struct {
	self     int64
	dialogs  []tgclient.Dialog
	targets  []tgclient.Target
	warnings []string
	notes    []string
}

func selectChats(ctx context.Context, api *tgclient.API, raw *tg.Client, cfg config.Config, log *slog.Logger) (chatSelection, error) {
	self, err := tgclient.SelfID(ctx, raw)
	if err != nil {
		return chatSelection{}, err
	}
	dialogs, err := api.Dialogs(ctx)
	if err != nil {
		return chatSelection{}, err
	}
	tgclient.NameSelf(dialogs, self)
	folders, err := api.Folders(ctx, self)
	if err != nil {
		return chatSelection{}, err
	}
	targets, warnings, err := tgclient.SelectTargets(dialogs, folders, cfg.Targets)
	if err != nil {
		return chatSelection{}, err
	}
	targets, migrationWarnings := api.AddMigrated(ctx, targets)
	for _, w := range migrationWarnings {
		log.Warn(w)
	}
	log.Info(fmt.Sprintf("archiving %s", plural(len(targets), "chat")))
	notes := tgclient.TopicExclusionNotes(targets, cfg.Targets)
	return chatSelection{self: self, dialogs: dialogs, targets: targets, warnings: warnings, notes: notes}, nil
}

// dryRun resolves targets like sync but touches neither run.lock nor the archive.
func dryRun(ctx context.Context, st setup, env Env, log *slog.Logger) int {
	var (
		chats           []stats.ChatStats
		warnings, notes []string
	)
	err := tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		api := tgclient.NewAPI(c.API())
		sel, err := selectChats(ctx, api, c.API(), st.cfg, log)
		if err != nil {
			return err
		}
		chats = stats.Collect(ctx, api, statItems(sel.targets), progressPrinter(env.Stderr, isTerminal(env.Stderr), "counting"))
		printReport(env.Stdout, chats)
		warnings, notes = sel.warnings, sel.notes
		return nil
	})
	for _, w := range append(slices.Clone(warnings), notes...) {
		fmt.Fprintf(env.Stdout, "warning: %s\n", w)
	}
	if err != nil {
		log.Error("dry run failed", "err", err.Error())
		return 1
	}
	return dryRunExit(chats, warnings)
}

func dryRunExit(chats []stats.ChatStats, warnings []string) int {
	if len(warnings) > 0 {
		return 1
	}
	for _, c := range chats {
		if c.Err != nil {
			return 1
		}
	}
	return 0
}

// runSync reports progress to progressOut: per chat, and per media file.
func runSync(ctx context.Context, raw *tg.Client, db *store.Store, cfg config.Config, log *slog.Logger, progressOut io.Writer) (syncSummary, error) {
	tty := isTerminal(progressOut)
	api := tgclient.NewAPI(raw)
	sel, err := selectChats(ctx, api, raw, cfg, log)
	if err != nil {
		return syncSummary{}, err
	}
	self, dialogs, targets, warnings := sel.self, sel.dialogs, sel.targets, sel.warnings

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
		Progress: chatProgress(progressOut, tty),
	}
	sum := syncSummary{sync: s.Run(ctx, targets, dialogIDs), unresolved: warnings, notes: sel.notes}
	// The pages need no Telegram: messages saved before a lost session or a
	// failed media phase still show up in them.
	views := func() {
		sum.render, sum.renderErr = renderArchive(ctx, db, cfg.ArchiveDir, false)
		sum.viewsDone = true
	}
	if sum.sync.AuthErr != nil {
		views()
		return sum, sum.sync.AuthErr
	}

	d := media.Downloader{
		TG: api, Store: db, ArchiveDir: cfg.ArchiveDir, Peers: peers, Workers: cfg.Media.Workers, SelfID: self,
		MaxFileSize: int64(cfg.Media.MaxFileSize), SkipKinds: cfg.Media.SkipKinds, Log: log,
		Progress: progressPrinter(progressOut, tty, "media"),
	}
	sum.media, err = d.Run(ctx)
	// Not a failed run, but worth seeing in the run's details.
	for _, e := range sum.media.RetryErrors {
		log.Warn("a file will be downloaded again next run", "err", e.Error())
	}
	views()
	if err != nil {
		return sum, err
	}
	if sum.media.AuthErr != nil {
		return sum, sum.media.AuthErr
	}
	return sum, nil
}
