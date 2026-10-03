package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// Snapshot is one tgbup snapshot in a repository.
type Snapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
	Host    string    `json:"hostname"`
	// Paths are the absolute paths backed up: the work folder with the
	// database copy and the config, and the archive's media/.
	Paths []string `json:"paths"`
}

// Snapshots lists the tgbup snapshots in repo, newest first.
func Snapshots(ctx context.Context, o Options, repo string) ([]Snapshot, error) {
	out, err := o.restic(ctx, repo, "snapshots", "--json", "--tag", "tgbup")
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("read the snapshot list of %s: %w", repo, err)
	}
	slices.SortFunc(snaps, func(a, b Snapshot) int { return b.Time.Compare(a.Time) })
	return snaps, nil
}

// RestoreResult says what a restore put where.
type RestoreResult struct {
	Snapshot Snapshot
	// MovedAside is where the database that was in the archive folder went; "" if there was none.
	MovedAside string
	// MediaAdded files came from the snapshot; MediaKept were already there and left alone.
	MediaAdded, MediaKept int
	// Config is the snapshot's config.yaml; the caller decides whether to use it.
	Config []byte
}

// Restore brings a snapshot ("latest" or an ID) back into archiveDir: the
// database, and the media files that are missing there. Nothing is deleted:
// a database already in place is renamed aside, existing media stay. The
// snapshot is restored next to the archive first, so moving it into place
// is a rename, not a second copy of the media.
func Restore(ctx context.Context, o Options, repo, snapshotID, archiveDir string, now time.Time) (RestoreResult, error) {
	var res RestoreResult
	snaps, err := Snapshots(ctx, o, repo)
	if err != nil {
		return res, err
	}
	if len(snaps) == 0 {
		return res, fmt.Errorf("%s has no tgbup snapshots", repo)
	}
	i := 0
	if snapshotID != "" && snapshotID != "latest" {
		i = slices.IndexFunc(snaps, func(s Snapshot) bool { return s.ID == snapshotID || s.ShortID == snapshotID })
		if i < 0 {
			return res, fmt.Errorf("no snapshot %s in %s", snapshotID, repo)
		}
	}
	res.Snapshot = snaps[i]

	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return res, err
	}
	// Left by a restore that was killed; only tgbup writes these folders.
	if stale, _ := filepath.Glob(filepath.Join(archiveDir, ".tgbup-restore-*")); len(stale) > 0 {
		for _, d := range stale {
			_ = os.RemoveAll(d)
		}
	}
	tmp := filepath.Join(archiveDir, ".tgbup-restore-"+now.Format("20060102-150405"))
	defer os.RemoveAll(tmp)
	if _, err := o.restic(ctx, repo, "restore", res.Snapshot.ID, "--target", tmp); err != nil {
		return res, err
	}

	var work, media string
	for _, p := range res.Snapshot.Paths {
		restored := filepath.Join(tmp, p)
		switch {
		case filepath.Base(p) == "media":
			media = restored
		case isFile(filepath.Join(restored, "archive.sqlite")):
			work = restored
		}
	}
	if work == "" {
		return res, fmt.Errorf("snapshot %s has no archive.sqlite", res.Snapshot.ShortID)
	}
	if res.Config, err = os.ReadFile(filepath.Join(work, "config.yaml")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}

	db := filepath.Join(archiveDir, "archive.sqlite")
	if isFile(db) {
		res.MovedAside = db + ".before-restore-" + now.Format("20060102-150405")
		// The WAL files belong to the old database and must not meet the restored one.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if isFile(db + suffix) {
				if err := os.Rename(db+suffix, res.MovedAside+suffix); err != nil {
					return res, err
				}
			}
		}
	}
	if err := moveFile(filepath.Join(work, "archive.sqlite"), db); err != nil {
		return res, err
	}
	if media != "" {
		if res.MediaAdded, res.MediaKept, err = mergeMedia(media, filepath.Join(archiveDir, "media")); err != nil {
			where := "there was none before"
			if res.MovedAside != "" {
				where = "the previous one is " + res.MovedAside
			}
			return res, fmt.Errorf("the restored database is in place (%s), but moving the media stopped after %d files: %w; run the restore again to add the rest",
				where, res.MediaAdded, err)
		}
	}
	return res, nil
}

// mergeMedia moves the files of src missing in dst into it.
func mergeMedia(src, dst string) (added, kept int, err error) {
	if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) && os.Rename(src, dst) == nil {
		err = filepath.WalkDir(dst, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				added++
			}
			return err
		})
		return added, 0, err
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if _, err := os.Lstat(to); err == nil {
			kept++
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := moveFile(p, to); err != nil {
			return err
		}
		added++
		return nil
	})
	return added, kept, err
}

// moveFile renames, or copies when the target is on another volume (media/
// may be a link to an external disk).
func moveFile(from, to string) error {
	err := os.Rename(from, to)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	part := to + ".part"
	dst, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(part)
		return err
	}
	if err := errors.Join(dst.Sync(), dst.Close()); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, to); err != nil {
		return err
	}
	return os.Remove(from)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
