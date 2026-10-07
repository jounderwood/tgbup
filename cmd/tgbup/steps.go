package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"golang.org/x/sys/unix"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/diskcrypt"
	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/serve"
	"gitlab.com/joeunderwood/tgbup/internal/render"
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
	if err := checkTestIsolation(env); err != nil {
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
	if st.testDC && p == expandTilde(defaultBackupDir, st.paths.Home) {
		return "", badInput("on Telegram's test servers use a test folder, not the real backup folder %s", defaultBackupDir)
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

// resticSchemes are the repository addresses restic understands besides a folder.
var resticSchemes = []string{"local:", "sftp:", "rest:", "s3:", "azure:", "gs:", "b2:", "swift:", "rclone:", "opendal:"}

// errNotAPlace is deliberately without the value: what was typed may be the
// password, pasted into the wrong field.
var errNotAPlace = badInput("a backup place is a full path (/… or ~/…) or a restic repository (sftp:, s3:, rclone:…)")

// checkRepo accepts a place restic finds from any folder. Anything else would
// be a folder relative to wherever tgbup runs, created there by restic.
func checkRepo(repo, password string) error {
	if repo == "" || password != "" && strings.Contains(repo, password) {
		return errNotAPlace
	}
	if filepath.IsAbs(repo) {
		return nil
	}
	for _, s := range resticSchemes {
		if len(repo) > len(s) && strings.HasPrefix(repo, s) {
			return nil
		}
	}
	return errNotAPlace
}

// moveArchive moves the archive folder, with its database, media and pages,
// to dir and points the config there. On the same disk it is a rename; to
// another disk the files are copied, checked and only then removed from the
// old place. Runs wait meanwhile (run.lock).
func moveArchive(st setup, dir string) error {
	if err := checkArchiveDir(dir); err != nil {
		return err
	}
	from, to := filepath.Clean(st.cfg.ArchiveDir), filepath.Clean(expandTilde(dir, st.paths.Home))
	switch {
	case from == to:
		return badInput("the archive is already there")
	case within(to, from) || within(from, to):
		return badInput("choose a folder outside the archive, not one containing it")
	case within(to, st.paths.StateDir):
		return badInput("choose a folder apart from tgbup's own files")
	}
	for _, r := range st.cfg.Backup.Repositories {
		if filepath.IsAbs(r) && (within(r, to) || within(to, r)) {
			return badInput("choose a folder apart from the backup folder %s", tildePath(r, st.paths.Home))
		}
	}
	if entries, err := os.ReadDir(to); err == nil && len(entries) > 0 {
		return badInput("%s is not empty: choose a new or empty folder", tildePath(to, st.paths.Home))
	}
	release, err := acquireLock(st.paths.Lock())
	if errors.Is(err, errLocked) {
		return badInput("a run is in progress: move the archive when it has finished")
	}
	if err != nil {
		return err
	}
	defer release()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	_ = os.Remove(to) // an empty folder chosen in the dialog: the rename takes its place
	err = os.Rename(from, to)
	if errors.Is(err, syscall.EXDEV) {
		err = copyArchive(from, to)
	}
	if err != nil {
		return fmt.Errorf("the archive was not moved and is still in %s: %w", tildePath(from, st.paths.Home), err)
	}
	if err := saveArchiveDir(st, to); err != nil {
		return fmt.Errorf("the archive is now in %s, but the config could not be updated: %w; set archive_dir to it", tildePath(to, st.paths.Home), err)
	}
	return nil
}

// copyArchive copies from to to (another disk), checks every file arrived
// whole, and only then removes from. A failed copy leaves from as it was.
func copyArchive(from, to string) error {
	var files, bytes int64
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case info.Mode().IsRegular():
			files++
			bytes += info.Size()
			return copyFileKeepingTimes(p, dst, info)
		}
		return nil
	})
	if err == nil {
		var gotFiles, gotBytes int64
		err = filepath.WalkDir(to, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				info, ierr := d.Info()
				if ierr != nil {
					return ierr
				}
				gotFiles++
				gotBytes += info.Size()
			}
			return err
		})
		if err == nil && (gotFiles != files || gotBytes != bytes) {
			err = fmt.Errorf("the copy has %d files, %d bytes; the archive %d files, %d bytes", gotFiles, gotBytes, files, bytes)
		}
	}
	if err != nil {
		_ = os.RemoveAll(to)
		return err
	}
	return os.RemoveAll(from)
}

func copyFileKeepingTimes(from, to string, info fs.FileInfo) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := errors.Join(dst.Sync(), dst.Close()); err != nil {
		return err
	}
	// The macOS "downloaded" mark of someone else's file goes along.
	if n, err := unix.Getxattr(from, "com.apple.quarantine", make([]byte, 0)); err == nil && n > 0 {
		buf := make([]byte, n)
		if n, err = unix.Getxattr(from, "com.apple.quarantine", buf); err == nil {
			_ = unix.Setxattr(to, "com.apple.quarantine", buf[:n], 0)
		}
	}
	return os.Chtimes(to, info.ModTime(), info.ModTime())
}

// notifySetup tells the bot that tgbup is set up (the schedule, the last
// step, is in place) and what it will do. Only logged when it fails.
func notifySetup(ctx context.Context, st setup, log func(error)) {
	bot := notify.Bot{Token: st.secrets.BotToken, ChatID: st.secrets.BotChatID}
	if !st.cfg.Notify.Progress || !bot.Enabled() {
		return
	}
	places, _ := splitPlaces(st)
	for i, r := range places {
		places[i] = tildePath(r, st.paths.Home)
	}
	text := notify.SetupText(hostname(), len(st.cfg.Targets.Include), st.cfg.Targets.Folders, places, clockList(st.cfg.Schedule.Clocks()))
	if err := bot.Send(ctx, text); err != nil {
		log(err)
	}
}

// diskWarning is what to say when the disk holding the archive is not
// encrypted; "" when it is, or when that cannot be told.
func diskWarning(ctx context.Context, archiveDir string) string {
	dir := archiveDir
	for !isDir(dir) && filepath.Dir(dir) != dir {
		dir = filepath.Dir(dir) // not created yet: the disk it will be on
	}
	r := diskcrypt.Check(ctx, dir)
	if r.State != diskcrypt.Plain {
		return ""
	}
	return "The disk with the archive is not encrypted: anyone who gets hold of it can read your chats. To fix: " + r.Hint + "."
}

// splitPlaces separates the configured places restic can use from those that
// are not places at all (a password pasted by mistake), counted, never shown.
func splitPlaces(st setup) (good []string, bad int) {
	for _, r := range st.cfg.Backup.Repositories {
		if checkRepo(r, st.secrets.ResticPassword) == nil {
			good = append(good, r)
		} else {
			bad++
		}
	}
	return good, bad
}

// saveBackup writes the backup places and, when given, a new password. A
// password replacing one in use must open the repositories that already
// exist: a typo would otherwise replace the working password.
func saveBackup(ctx context.Context, st setup, repos []string, password string) error {
	if len(repos) == 0 {
		return badInput("choose at least one backup place")
	}
	for _, r := range repos {
		if err := errors.Join(checkRepo(r, password), checkRepo(r, st.secrets.ResticPassword)); err != nil {
			return errNotAPlace
		}
	}
	if password == "" && st.secrets.ResticPassword == "" {
		return badInput("set the backup password")
	}
	if password != "" {
		if err := checkPassword(password); err != nil {
			return err
		}
		if st.secrets.ResticPassword != "" && password != st.secrets.ResticPassword {
			if err := checkOpens(ctx, st, repos, password); err != nil {
				return err
			}
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

// checkOpens checks that password opens each repository that exists.
func checkOpens(ctx context.Context, st setup, repos []string, password string) error {
	st.secrets.ResticPassword = password
	st.cfg.Backup.Repositories = repos
	o, err := backupOptions(ctx, st)
	if err != nil {
		return err
	}
	for _, r := range backup.Opens(ctx, o) {
		if r.Err != nil {
			return badInput("the new password does not open %s: keep the one the repositories were made with", tildePath(r.Repo, st.paths.Home))
		}
	}
	return nil
}

// saveScheduleTimes writes schedule.times from "06:30, 18:30"; the job must
// then be installed again to use them.
func saveScheduleTimes(st setup, times string) error {
	clocks, err := config.ParseTimes(splitTimes(times))
	if err != nil {
		return badInput("%v", err)
	}
	return config.SetValue(st.paths.Config, []string{"schedule", "times"}, strings.Split(clockList(clocks), ", "))
}

// splitTimes reads times separated by commas or spaces.
func splitTimes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
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

// restoreOutcome is a restore and what it changed in the setup.
type restoreOutcome struct {
	backup.RestoreResult
	// ConfigAdopted: the snapshot's config replaced an empty one (no chats yet).
	ConfigAdopted bool
	// PasswordKept: the password differs from the one in use, so the place was
	// not added to the backups and the current password stays.
	PasswordKept bool
}

// restoreCopy restores a snapshot into its own folder next to the archive,
// only to be looked at: unlike restoreArchive it leaves the config, the
// secrets and the archive alone, and needs no run.lock, so runs go on. The
// marker file says the copy is complete.
func restoreCopy(ctx context.Context, st setup, repo, password, snapshot string) (string, error) {
	repo = expandTilde(repo, st.paths.Home)
	o, err := restoreOptions(ctx, st, repo, password)
	if err != nil {
		return "", err
	}
	snaps, err := backup.Snapshots(ctx, o, repo)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(snaps, func(s backup.Snapshot) bool { return s.ID == snapshot || s.ShortID == snapshot })
	if i < 0 {
		return "", badInput("no snapshot %s in %s", snapshot, tildePath(repo, st.paths.Home))
	}
	// The folder is named by the short id, whatever named the snapshot.
	dir := render.SnapshotCopyDir(st.cfg.ArchiveDir, snaps[i].ShortID)
	if _, err := backup.Restore(ctx, o, repo, snaps[i].ID, dir, time.Now()); err != nil {
		return "", err
	}
	db, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := renderArchive(ctx, db, dir, true); err != nil {
		return "", fmt.Errorf("restored, but drawing the pages failed: %w", err)
	}
	return dir, os.WriteFile(filepath.Join(dir, render.CopyDone), nil, 0o644)
}

// restoreArchive brings a snapshot back into archiveDir (or the configured
// one) and redraws the pages. Nothing is deleted. The password that opened
// the snapshot is saved only if none was set; a config with chats is left as
// it is, an empty one is replaced by the snapshot's.
func restoreArchive(ctx context.Context, st setup, repo, password, snapshot, archiveDir string) (restoreOutcome, error) {
	var out restoreOutcome
	repo = expandTilde(repo, st.paths.Home)
	o, err := restoreOptions(ctx, st, repo, password)
	if err != nil {
		return out, err
	}
	if archiveDir == "" {
		archiveDir = st.cfg.ArchiveDir
	}
	if err := checkArchiveDir(archiveDir); err != nil {
		return out, err
	}
	archiveDir = expandTilde(archiveDir, st.paths.Home)
	release, err := acquireLock(st.paths.Lock())
	if err != nil {
		return out, fmt.Errorf("%w: wait for it to finish and try again", err)
	}
	defer release()
	out.RestoreResult, err = backup.Restore(ctx, o, repo, snapshot, archiveDir, time.Now())
	if err != nil {
		if strings.Contains(err.Error(), "wrong password") {
			return out, badInput("the password does not open %s", tildePath(repo, st.paths.Home))
		}
		return out, err
	}
	keepRepo := true
	switch current := st.secrets.ResticPassword; {
	case current == "":
		if err := config.SetSecrets(st.paths.Secrets, map[string]string{"RESTIC_PASSWORD": password}); err != nil {
			return out, err
		}
	case current != password:
		// The current password opens the current backups; one password serves them all.
		keepRepo, out.PasswordKept = false, true
	}
	if !keepRepo {
		repo = ""
	}
	if out.ConfigAdopted, err = adoptRestoredConfig(st, out.Config, archiveDir, repo); err != nil {
		return out, err
	}
	db, err := store.Open(filepath.Join(archiveDir, "archive.sqlite"))
	if err != nil {
		return out, err
	}
	defer db.Close()
	if _, err := renderArchive(ctx, db, archiveDir, true); err != nil {
		return out, fmt.Errorf("restored, but drawing the pages failed: %w", err)
	}
	return out, nil
}

// adoptRestoredConfig takes the snapshot's config when the current one has
// no chats yet (a new Mac, perhaps with backups set up first), pointing it at
// the archive folder chosen now and keeping the backup places of both. A
// config with chats keeps its archive folder and settings. repo, when given,
// becomes a backup place.
func adoptRestoredConfig(st setup, snapshotConfig []byte, archiveDir, repo string) (bool, error) {
	home := st.paths.Home
	current, err := config.LoadWithoutTargets(st.paths.Config, home)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	empty := !exists || (len(current.Targets.Include) == 0 && len(current.Targets.Folders) == 0)
	adopt := empty && len(snapshotConfig) > 0
	repos := current.Backup.Repositories
	if adopt {
		if err := os.MkdirAll(filepath.Dir(st.paths.Config), 0o700); err != nil {
			return false, err
		}
		if err := os.WriteFile(st.paths.Config, bytes.TrimSpace(snapshotConfig), 0o600); err != nil {
			return false, err
		}
		if err := config.SetValue(st.paths.Config, []string{"archive_dir"}, tildePath(archiveDir, home)); err != nil {
			return false, err
		}
		restored, err := config.LoadWithoutTargets(st.paths.Config, home)
		if err != nil {
			return false, err
		}
		repos = append(slices.Clone(restored.Backup.Repositories), repos...)
	}
	if repo != "" {
		repos = append(repos, repo)
	}
	var shown []string
	for _, r := range repos {
		if t := tildePath(expandTilde(r, home), home); !slices.Contains(shown, t) {
			shown = append(shown, t)
		}
	}
	if !adopt && slices.Equal(shown, tildeAll(current.Backup.Repositories, home)) {
		return false, nil
	}
	return adopt, config.SetValue(st.paths.Config, []string{"backup", "repositories"}, shown)
}

func tildeAll(paths []string, home string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = tildePath(p, home)
	}
	return out
}
