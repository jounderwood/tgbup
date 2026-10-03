package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/serve"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// The setup steps without their questions: tgbup init asks in the terminal,
// tgbup serve in the browser, and both call these.

// inputError is a mistake in what the person entered: the terminal asks
// again, the web page answers 400 with the message.
type inputError struct{ msg string }

func (e inputError) Error() string        { return e.msg }
func (e inputError) Is(target error) bool { return target == serve.ErrBadRequest }

func badInput(format string, args ...any) error { return inputError{fmt.Sprintf(format, args...)} }

var apiHashRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func checkAPIID(v string) error {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n <= 0 {
		return badInput("api_id is a number, e.g. 12345678")
	}
	return nil
}

func checkAPIHash(v string) error {
	if !apiHashRe.MatchString(strings.ToLower(strings.TrimSpace(v))) {
		return badInput("api_hash is 32 characters 0-9 and a-f")
	}
	return nil
}

// saveKeys writes the Telegram API keys. It needs no loadable setup: it is
// how broken keys get replaced.
func saveKeys(env Env, id, hash string) (string, error) {
	if err := errors.Join(checkAPIID(id), checkAPIHash(hash)); err != nil {
		return "", err
	}
	paths, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return "", err
	}
	return paths.Secrets, config.SetSecrets(paths.Secrets, map[string]string{
		"TG_API_ID": strings.TrimSpace(id), "TG_API_HASH": strings.ToLower(strings.TrimSpace(hash))})
}

func checkArchiveDir(dir string) error {
	if strings.HasPrefix(dir, "~/") || dir == "~" || filepath.IsAbs(dir) {
		return nil
	}
	return badInput("give a full path or one starting with ~/")
}

func saveArchiveDir(st setup, dir string) error {
	if err := checkArchiveDir(dir); err != nil {
		return err
	}
	return config.SetValue(st.paths.Config, []string{"archive_dir"}, tildePath(expandTilde(dir, st.paths.Home), st.paths.Home))
}

// checkBackupDir accepts a folder on this Mac apart from the archive and
// tgbup's own files: a backup inside them would back up itself.
func checkBackupDir(st setup, dir string) (string, error) {
	p := expandTilde(strings.TrimSpace(dir), st.paths.Home)
	if !filepath.IsAbs(p) {
		return "", badInput("give a full path or one starting with ~/")
	}
	archive := expandTilde(st.cfg.ArchiveDir, st.paths.Home)
	if within(p, archive) || within(archive, p) || within(p, st.paths.StateDir) {
		return "", badInput("choose a folder apart from the archive and tgbup's own files")
	}
	return p, nil
}

func checkPassword(p string) error {
	switch {
	case len([]rune(p)) < 12:
		return badInput("the password needs at least 12 characters")
	case !config.SecretRoundTrips(p) || strings.ContainsAny(p, "\r\n"):
		return badInput("the password must not start or end with a space or a quote")
	}
	return nil
}

// saveBackup writes the backup places and, when given, a new password.
func saveBackup(st setup, repos []string, password string) error {
	if len(repos) == 0 {
		return badInput("choose at least one backup place")
	}
	if password == "" && st.secrets.ResticPassword == "" {
		return badInput("set the backup password")
	}
	if password != "" {
		if err := checkPassword(password); err != nil {
			return err
		}
		if err := config.SetSecrets(st.paths.Secrets, map[string]string{"RESTIC_PASSWORD": password}); err != nil {
			return err
		}
	}
	shown := make([]string, len(repos))
	for i, r := range repos {
		shown[i] = tildePath(r, st.paths.Home)
	}
	return config.SetValue(st.paths.Config, []string{"backup", "repositories"}, shown)
}

// disableBackup forgets the places; the snapshots made so far stay.
func disableBackup(st setup) error {
	return config.SetValue(st.paths.Config, []string{"backup", "repositories"}, []string{})
}

// saveBot sends a test message and keeps the bot only if it arrived.
func saveBot(ctx context.Context, st setup, bot notify.Bot) error {
	if err := bot.Send(ctx, "✅ tgbup on "+hostname()+": alerts will come here"); err != nil {
		return err
	}
	return config.SetSecrets(st.paths.Secrets, map[string]string{"BOT_TOKEN": bot.Token, "BOT_CHAT_ID": bot.ChatID})
}

// restoreOptions is backupOptions for one repository and a password that may
// not be saved yet.
func restoreOptions(ctx context.Context, st setup, repo, password string) (backup.Options, error) {
	st.secrets.ResticPassword = password
	if password == "" {
		return backup.Options{}, badInput("enter the backup password")
	}
	st.cfg.Backup.Repositories = []string{repo}
	return backupOptions(ctx, st)
}

func listSnapshots(ctx context.Context, st setup, repo, password string) ([]backup.Snapshot, error) {
	o, err := restoreOptions(ctx, st, expandTilde(repo, st.paths.Home), password)
	if err != nil {
		return nil, err
	}
	snaps, err := backup.Snapshots(ctx, o, o.Repos[0])
	if err != nil && strings.Contains(err.Error(), "wrong password") {
		return nil, badInput("the password does not open %s", repo)
	}
	return snaps, err
}

// restoreArchive brings a snapshot back into archiveDir (or the configured
// one), keeps the password that opened it, writes the snapshot's config if
// there is none yet, and rebuilds the views. Nothing is deleted.
func restoreArchive(ctx context.Context, st setup, repo, password, snapshot, archiveDir string) (backup.RestoreResult, error) {
	repo = expandTilde(repo, st.paths.Home)
	o, err := restoreOptions(ctx, st, repo, password)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	if archiveDir == "" {
		archiveDir = st.cfg.ArchiveDir
	}
	if err := checkArchiveDir(archiveDir); err != nil {
		return backup.RestoreResult{}, err
	}
	archiveDir = expandTilde(archiveDir, st.paths.Home)
	release, err := acquireLock(st.paths.Lock())
	if err != nil {
		return backup.RestoreResult{}, fmt.Errorf("%w: wait for it to finish and try again", err)
	}
	defer release()
	res, err := backup.Restore(ctx, o, repo, snapshot, archiveDir, time.Now())
	if err != nil {
		if strings.Contains(err.Error(), "wrong password") {
			return res, badInput("the password does not open %s", tildePath(repo, st.paths.Home))
		}
		return res, err
	}
	if err := config.SetSecrets(st.paths.Secrets, map[string]string{"RESTIC_PASSWORD": password}); err != nil {
		return res, err
	}
	if err := adoptRestoredConfig(st, res.Config, archiveDir, repo); err != nil {
		return res, err
	}
	db, err := store.Open(filepath.Join(archiveDir, "archive.sqlite"))
	if err != nil {
		return res, err
	}
	defer db.Close()
	if _, err := rebuildFiles(ctx, db, archiveDir); err != nil {
		return res, fmt.Errorf("restored, but rebuilding files/ failed: %w", err)
	}
	if _, err := renderArchive(ctx, db, archiveDir, true); err != nil {
		return res, fmt.Errorf("restored, but drawing the pages failed: %w", err)
	}
	return res, nil
}

// adoptRestoredConfig writes the snapshot's config when there is none yet,
// pointing it at the archive folder chosen now, and makes sure the place the
// archive came from stays a backup place.
func adoptRestoredConfig(st setup, snapshotConfig []byte, archiveDir, repo string) error {
	if _, err := os.Stat(st.paths.Config); errors.Is(err, fs.ErrNotExist) && len(snapshotConfig) > 0 {
		if err := os.MkdirAll(filepath.Dir(st.paths.Config), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(st.paths.Config, bytes.TrimSpace(snapshotConfig), 0o600); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := config.SetValue(st.paths.Config, []string{"archive_dir"}, tildePath(archiveDir, st.paths.Home)); err != nil {
		return err
	}
	cfg, err := config.LoadWithoutTargets(st.paths.Config, st.paths.Home)
	if err != nil {
		return err
	}
	if slices.Contains(cfg.Backup.Repositories, repo) {
		return nil
	}
	repos := append(slices.Clone(cfg.Backup.Repositories), repo)
	for i, r := range repos {
		repos[i] = tildePath(r, st.paths.Home)
	}
	return config.SetValue(st.paths.Config, []string{"backup", "repositories"}, repos)
}
