package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// database copy and the config, and the archive's media/ and html/.
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
	// MediaAdded files came from the snapshot; MediaKept were already there,
	// the same, and left alone.
	MediaAdded, MediaKept int
	// MediaReplaced files were already there under a snapshot file's name but
	// differ (names are not ids); they were moved to MediaAside.
	MediaReplaced int
	MediaAside    string
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

	// Only the owner may read an archive: on a shared Mac or a NAS the files
	// inside keep their usual 0644.
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
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
	args := []string{"restore", res.Snapshot.ID, "--target", tmp}
	for _, p := range res.Snapshot.Paths {
		// The pages are redrawn after the restore; html/ is in snapshots for
		// reading them without tgbup.
		if filepath.Base(p) == "html" {
			args = append(args, "--exclude", p)
		}
	}
	if _, err := o.restic(ctx, repo, args...); err != nil {
		return res, err
	}

	var work, media string
	for _, p := range res.Snapshot.Paths {
		restored := filepath.Join(tmp, p)
		// The paths come from the snapshot's own record: one made by someone
		// else could point outside the restore, or at a link out of it.
		if rel, err := filepath.Rel(tmp, restored); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return res, fmt.Errorf("snapshot %s lists a path outside it: %q", res.Snapshot.ShortID, p)
		}
		if fi, err := os.Lstat(restored); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return res, fmt.Errorf("snapshot %s: %q is a link", res.Snapshot.ShortID, p)
		}
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
		aside := filepath.Join(archiveDir, "media.before-restore-"+now.Format("20060102-150405"))
		if res.MediaAdded, res.MediaKept, res.MediaReplaced, err = mergeMedia(media, filepath.Join(archiveDir, "media"), aside); res.MediaReplaced > 0 {
			res.MediaAside = aside
		}
		if err != nil {
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

// mergeMedia moves the files of src into dst. A file already in dst with
// the same content stays; one with other content under the same name is
// moved to aside, so the restored database finds its own files there.
func mergeMedia(src, dst, aside string) (added, kept, replaced int, err error) {
	if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) && os.Rename(src, dst) == nil {
		err = filepath.WalkDir(dst, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				added++
			}
			return err
		})
		return added, 0, 0, err
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
			same, err := sameContent(p, to)
			if err != nil {
				return err
			}
			if same {
				kept++
				return nil
			}
			away := filepath.Join(aside, rel)
			if err := os.MkdirAll(filepath.Dir(away), 0o755); err != nil {
				return err
			}
			if err := os.Rename(to, away); err != nil {
				return err
			}
			replaced++
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
	return added, kept, replaced, err
}

// sameContent compares two files byte by byte.
func sameContent(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	sa, err := fa.Stat()
	if err != nil {
		return false, err
	}
	sb, err := fb.Stat()
	if err != nil {
		return false, err
	}
	if sa.Size() != sb.Size() {
		return false, nil
	}
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		if ea == io.EOF || ea == io.ErrUnexpectedEOF {
			return eb == ea, nil
		}
		if ea != nil {
			return false, ea
		}
		if eb != nil {
			return false, eb
		}
	}
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
	// Keep what the download set: the message date and the quarantine mark.
	if st, err := src.Stat(); err == nil {
		_ = os.Chtimes(part, st.ModTime(), st.ModTime())
	}
	copyQuarantine(from, part)
	if err := os.Rename(part, to); err != nil {
		return err
	}
	return os.Remove(from)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
