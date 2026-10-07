package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/deps"
	"gitlab.com/joeunderwood/tgbup/internal/serve"
)

// cmdRestore brings the archive back from a backup without running restic by hand.
func cmdRestore(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	from := fs.String("from", "", "backup place to restore from (default: asked, or the only one in the config)")
	snapshot := fs.String("snapshot", "", "snapshot ID to restore, or latest (default: asked)")
	to := fs.String("to", "", "archive folder to restore into (default: archive_dir from the config)")
	list := fs.Bool("list", false, "only list the snapshots")
	asCopy := fs.Bool("copy", false, "restore a copy to look at, next to the archive; the archive and the config stay as they are")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *asCopy && *to != "" {
		fmt.Fprintln(env.Stderr, "tgbup restore: --copy goes next to the archive by itself; leave out --to")
		return 2
	}
	w := &wizard{env: env, out: env.Stdout,
		in: newLineReader(env.Stdin, func(err error) { fmt.Fprintln(env.Stderr, "read input:", err) })}
	st, err := w.load()
	if err == nil {
		err = w.restoreWith(ctx, st, restoreRequest{repo: *from, snapshot: *snapshot, dir: *to, listOnly: *list, asCopy: *asCopy})
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errStopped):
		fmt.Fprintln(env.Stderr, "tgbup restore: stopped, nothing changed")
	default:
		fmt.Fprintln(env.Stderr, "tgbup restore:", err)
	}
	return 1
}

type restoreRequest struct {
	repo, snapshot, dir string
	listOnly            bool
	// asCopy: a copy to look at (restoreCopy), not the archive brought back.
	asCopy bool
}

// restore is the tgbup init way in: everything is asked.
func (w *wizard) restore(ctx context.Context, st setup, dir string) error {
	return w.restoreWith(ctx, st, restoreRequest{dir: dir})
}

func (w *wizard) restoreWith(ctx context.Context, st setup, req restoreRequest) error {
	home := st.paths.Home
	repo := req.repo
	if repo == "" && len(st.cfg.Backup.Repositories) == 1 {
		repo = st.cfg.Backup.Repositories[0]
	}
	if repo == "" {
		var err error
		if repo, err = w.askBackupPlace(ctx, st); err != nil {
			return err
		}
	}
	repo = expandTilde(repo, home)
	if err := ensureRcloneFor(ctx, st, repo, w); err != nil {
		return err
	}
	password := st.secrets.ResticPassword
	var snaps []backup.Snapshot
	for {
		if password == "" {
			var err error
			if password, err = w.readSecret(ctx, "  Backup password (not shown): "); err != nil {
				return err
			}
		}
		fmt.Fprintln(w.out, "  reading the backups (restic is downloaded once if needed)…")
		var err error
		snaps, err = listSnapshots(ctx, st, repo, password)
		if errors.Is(err, serve.ErrBadRequest) {
			fmt.Fprintf(w.out, "  %v\n", err)
			password = ""
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	if len(snaps) == 0 {
		return fmt.Errorf("%s has no tgbup snapshots", tildePath(repo, home))
	}
	fmt.Fprintf(w.out, "  Snapshots in %s (newest first):\n", tildePath(repo, home))
	for i, s := range snaps[:min(len(snaps), 10)] {
		fmt.Fprintf(w.out, "  %2d) %s  %s  from %s\n", i+1, s.Time.Local().Format("2006-01-02 15:04"), s.ShortID, s.Host)
	}
	if req.listOnly {
		return nil
	}
	snapshot := req.snapshot
	if snapshot == "latest" {
		snapshot = snaps[0].ID
	}
	if snapshot == "" {
		ans, err := w.askValid(ctx, "  Which one? [Enter = 1, the newest]: ", func(v string) error {
			if n, err := strconv.Atoi(v); v != "" && (err != nil || n < 1 || n > min(len(snaps), 10)) {
				return badInput("choose 1-%d", min(len(snaps), 10))
			}
			return nil
		})
		if err != nil {
			return err
		}
		snapshot = snaps[0].ID
		if ans != "" {
			n, _ := strconv.Atoi(ans)
			snapshot = snaps[n-1].ID
		}
	}
	if req.asCopy {
		fmt.Fprintln(w.out, "  restoring a copy next to the archive…")
		dir, err := restoreCopy(ctx, st, repo, password, snapshot)
		if err != nil {
			return err
		}
		refreshHistory(ctx, st.cfg.ArchiveDir)
		fmt.Fprintf(w.out, "  ✓ open it: %s\n", filepath.Join(tildePath(dir, home), "html", "index.html"))
		return nil
	}
	dir := req.dir
	if dir == "" {
		dir = st.cfg.ArchiveDir
	}
	dir = expandTilde(dir, home)
	if n, err := archivedMessages(ctx, dir); err == nil && n > 0 && !w.assumeYes {
		fmt.Fprintf(w.out, "  %s already holds %s; its database will be kept aside, not deleted.\n", tildePath(dir, home), plural(n, "message"))
		if ok, err := w.yes(ctx, "  Restore into it anyway?", false); err != nil {
			return err
		} else if !ok {
			return errStopped
		}
	}
	fmt.Fprintf(w.out, "  restoring into %s…\n", tildePath(dir, home))
	res, err := restoreArchive(ctx, st, repo, password, snapshot, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w.out, "  ✓ restored snapshot %s of %s: %s added", res.Snapshot.ShortID,
		res.Snapshot.Time.Local().Format("2006-01-02 15:04"), plural(res.MediaAdded, "media file"))
	if res.MediaKept > 0 {
		fmt.Fprintf(w.out, ", %s already there kept", plural(res.MediaKept, "file"))
	}
	fmt.Fprintln(w.out)
	if res.MovedAside != "" {
		fmt.Fprintf(w.out, "  the database that was there is kept as %s\n", res.MovedAside)
	}
	if res.MediaReplaced > 0 {
		fmt.Fprintf(w.out, "  %s there had the names of other files in the backup; they are kept in %s\n",
			plural(res.MediaReplaced, "media file"), tildePath(res.MediaAside, home))
	}
	if res.ConfigAdopted {
		fmt.Fprintln(w.out, "  the chats and settings from the backup are now the config")
	}
	if res.PasswordKept {
		fmt.Fprintf(w.out, "  %s opens with another password than your backups use now; the current one stays,\n  and that place was not added to the backups\n", tildePath(repo, home))
	}
	fmt.Fprintln(w.out, "  the pages are redrawn; log in to Telegram to keep the archive growing")
	return nil
}

// ensureRcloneFor connects the rclone remote of an rclone: repository (on a
// new Mac, Google Drive is signed into again in the browser).
func ensureRcloneFor(ctx context.Context, st setup, repo string, w *wizard) error {
	remote, ok := rcloneRemote(expandTilde(repo, st.paths.Home))
	if !ok {
		return nil
	}
	rclone, err := deps.NewFinder(st.paths.StateDir, st.cfg.Deps.Rclone).Ensure(ctx, deps.Rclone)
	if err != nil {
		return err
	}
	return w.ensureRcloneRemote(ctx, backup.Options{Rclone: rclone, RcloneConfigPass: st.secrets.RcloneConfigPass}, remote)
}

// askBackupPlace offers the places a backup may be in on this Mac.
func (w *wizard) askBackupPlace(ctx context.Context, st setup) (string, error) {
	home := st.paths.Home
	var places []storageChoice
	if def := expandTilde(backupDirDefault(st), home); isDir(def) {
		places = append(places, storageChoice{Label: tildePath(def, home) + " (folder on this Mac)", Repo: def})
	}
	places = append(places, storageChoices(home, "/Volumes")...)
	fmt.Fprintln(w.out, "  Where is the backup?")
	for i, c := range places {
		label := c.Label
		if c.Repo != "" && !isDir(c.Repo) && filepath.IsAbs(c.Repo) {
			label += " — no backup folder there"
		}
		fmt.Fprintf(w.out, "  %d) %s\n", i+1, label)
	}
	ans, err := w.askValid(ctx, "  Choose: ", func(v string) error {
		_, err := parseChoice(v, len(places))
		return err
	})
	if err != nil {
		return "", err
	}
	idx, _ := parseChoice(ans, len(places))
	if repo := places[idx[0]].Repo; repo != "" {
		return repo, nil
	}
	return w.askValid(ctx, "  Folder or repository: ", func(v string) error {
		if v == "" {
			return badInput("enter a folder or a restic repository")
		}
		if _, err := os.Stat(expandTilde(v, home)); err != nil && filepath.IsAbs(expandTilde(v, home)) {
			return badInput("%s does not exist", v)
		}
		return nil
	})
}
