package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/deps"
	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown host"
	}
	return strings.TrimSuffix(h, ".local")
}

// backupOptions resolves everything a backup needs, downloading restic (and
// rclone, for rclone: repositories) when they are not installed.
func backupOptions(ctx context.Context, st setup) (backup.Options, error) {
	b := st.cfg.Backup
	if st.secrets.ResticPassword == "" {
		return backup.Options{}, errors.New("RESTIC_PASSWORD is not set in secrets.env: without it the backups cannot be read back")
	}
	for _, r := range b.Repositories {
		if checkRepo(r, st.secrets.ResticPassword) != nil {
			return backup.Options{}, fmt.Errorf("%w; one in the config is not: remove it in tgbup init or on the Setup page (Backups → change)", errNotAPlace)
		}
	}
	o := backup.Options{
		Repos: b.Repositories, Retention: b.Retention, PruneEvery: b.PruneEvery.D(), CheckEvery: b.CheckEvery.D(),
		CheckSubset: b.CheckReadSubset, Password: st.secrets.ResticPassword, RcloneConfigPass: st.secrets.RcloneConfigPass,
		WorkDir: filepath.Join(st.paths.StateDir, "backup"), ArchiveDir: st.cfg.ArchiveDir, ConfigPath: st.paths.Config,
		Host: hostname(), Now: time.Now,
	}
	var err error
	if o.Restic, err = deps.NewFinder(st.paths.StateDir, st.cfg.Deps.Restic).Ensure(ctx, deps.Restic); err != nil {
		return o, err
	}
	for _, r := range o.Repos {
		if strings.HasPrefix(r, "rclone:") {
			if o.Rclone, err = deps.NewFinder(st.paths.StateDir, st.cfg.Deps.Rclone).Ensure(ctx, deps.Rclone); err != nil {
				return o, err
			}
			break
		}
	}
	return o, nil
}

// cmdDeps finds or downloads restic and rclone and shows where they are.
func cmdDeps(ctx context.Context, env Env) int {
	st, err := loadLocalSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup deps:", err)
		return 1
	}
	code := 0
	for _, t := range []struct {
		tool       deps.Tool
		configured string
	}{{deps.Restic, st.cfg.Deps.Restic}, {deps.Rclone, st.cfg.Deps.Rclone}} {
		path, err := deps.NewFinder(st.paths.StateDir, t.configured).Ensure(ctx, t.tool)
		if err != nil {
			fmt.Fprintf(env.Stderr, "%s: %v\n", t.tool.Name, err)
			code = 1
			continue
		}
		fmt.Fprintf(env.Stdout, "%s: %s (%s)\n", t.tool.Name, path, deps.Version(ctx, path))
	}
	return code
}

func cmdBackup(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	initRepos := fs.Bool("init", false, "create the repositories that do not exist yet")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := newLogger(env.Stderr)
	st, err := loadLocalSetup(env, true)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup backup:", err)
		return 1
	}
	if len(st.cfg.Backup.Repositories) == 0 {
		fmt.Fprintln(env.Stderr, "tgbup backup: no backup storage set up yet: run tgbup init")
		return 1
	}
	o, err := backupOptions(ctx, st)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup backup:", err)
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
	if *initRepos {
		code := 0
		for _, r := range backup.Init(ctx, o) {
			if r.Err != nil {
				fmt.Fprintf(env.Stdout, "%s: %v\n", r.Repo, r.Err)
				code = 1
				continue
			}
			fmt.Fprintf(env.Stdout, "%s: ready\n", r.Repo)
		}
		return code
	}
	db, err := store.Open(filepath.Join(st.cfg.ArchiveDir, "archive.sqlite"))
	if err != nil {
		log.Error("open archive", "err", err)
		return 1
	}
	defer db.Close()
	rep := backup.Run(ctx, db, o)
	printBackup(env.Stdout, rep)
	if !rep.OK() {
		fmt.Fprintln(env.Stdout, "hint: a new repository needs tgbup backup --init first")
		return 1
	}
	for _, r := range rep.Repos {
		if r.Warning != "" {
			return 1
		}
	}
	return 0
}

// cmdRun is what the schedule starts: sync, backup, record, notify.
func cmdRun(ctx context.Context, env Env) int {
	log := newLogger(env.Stderr)
	st, err := loadSetup(env, true)
	if err != nil {
		return failEarly(ctx, log, botFromSecrets(env), "load the setup", err)
	}
	bot := notify.Bot{Token: st.secrets.BotToken, ChatID: st.secrets.BotChatID}
	release, err := acquireLock(st.paths.Lock())
	if errors.Is(err, errLocked) {
		log.Info("skipping: another run is in progress")
		return 0
	}
	if err != nil {
		return failEarly(ctx, log, bot, "take run.lock", err)
	}
	defer release()
	db, err := store.Open(filepath.Join(st.cfg.ArchiveDir, "archive.sqlite"))
	if err != nil {
		return failEarly(ctx, log, bot, "open the archive", err)
	}
	defer db.Close()
	return runOnce(ctx, runParts{
		sync: func(ctx context.Context) (syncSummary, error) {
			return syncArchive(ctx, st, db, log, env.Stderr, false)
		},
		backup: func(ctx context.Context) (backup.Report, bool) {
			if len(st.cfg.Backup.Repositories) == 0 {
				return backup.Report{}, false
			}
			o, err := backupOptions(ctx, st)
			if err != nil {
				return backup.Report{Integrity: err}, true
			}
			return backup.Run(ctx, db, o), true
		},
		db: db, bot: bot,
		host: hostname(), archiveDir: st.cfg.ArchiveDir, repos: st.cfg.Backup.Repositories,
		summaryEvery: st.cfg.Notify.SummaryEvery.D(), now: time.Now, loc: time.Local,
		out: env.Stdout, log: log,
	})
}

func cmdNotify(ctx context.Context, args []string, env Env) int {
	if len(args) != 1 || args[0] != "test" {
		fmt.Fprintln(env.Stderr, "usage: tgbup notify test")
		return 2
	}
	st, err := loadLocalSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup notify:", err)
		return 1
	}
	bot := notify.Bot{Token: st.secrets.BotToken, ChatID: st.secrets.BotChatID}
	if !bot.Enabled() {
		fmt.Fprintln(env.Stderr, "tgbup notify: the alerts bot is not set up: run tgbup init")
		return 1
	}
	if err := bot.Send(ctx, "✅ tgbup on "+hostname()+": notifications work"); err != nil {
		fmt.Fprintln(env.Stderr, "tgbup notify:", err)
		return 1
	}
	fmt.Fprintln(env.Stdout, "sent")
	return 0
}

// failEarly reports a run that could not start: unattended, a broken config
// would otherwise fail every scheduled run with nobody told.
func failEarly(ctx context.Context, log *slog.Logger, bot notify.Bot, what string, err error) int {
	log.Error(what, "err", err)
	if bot.Enabled() {
		msg := notify.ErrorText(hostname(), notify.RunResult{SyncErrors: []string{what + ": " + err.Error()}})
		if serr := bot.Send(ctx, msg); serr != nil {
			log.Error("notify", "err", serr)
		}
	}
	return 1
}

// botFromSecrets reads only the secrets file, for when the rest of the setup failed to load.
func botFromSecrets(env Env) notify.Bot {
	p, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return notify.Bot{}
	}
	sec, _ := config.LoadSecrets(p.Secrets, env.Getenv)
	return notify.Bot{Token: sec.BotToken, ChatID: sec.BotChatID}
}
