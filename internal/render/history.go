package render

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joeunderwood/tgbup/internal/backup"
	"github.com/joeunderwood/tgbup/internal/store"
)

// historyLimit is how many updates history.html lists, newest first.
const historyLimit = 500

type historyPage struct {
	Updates []historyUpdate
	Older   int // updates before the listed ones
}

type historyUpdate struct {
	N    int
	When string   // when its snapshot was taken, else when the change was found
	IDs  []string // its snapshots that hold it, short
	// NoSnapshot says why there is none: the latest is not backed up yet, an
	// older one's changes went into the next snapshot.
	NoSnapshot string
	What       string
	Chats      []string // "Family +100", the chats that changed most
	Snapshots  []historySnap
}

type historySnap struct {
	Place, ID string
	Partial   bool   // some files could not be read
	Gone      bool   // retention removed it
	Restore   string // the command that opens this version in a folder of its own
	Open      string // the restored copy's index.html, relative; "" until restored
}

// updatedJS fills every ".asof" element with when the archive was drawn and,
// for the archive itself, the snapshot that holds it, linked to history.html.
const updatedJS = `window.tgbupUpdated = %s;
(() => {
  const here = document.currentScript && document.currentScript.src;
  const fmt = (t) => new Date(t).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  document.addEventListener('DOMContentLoaded', () => {
    const u = window.tgbupUpdated;
    if (u.anchor) for (const h of document.querySelectorAll('.history')) h.hidden = false;
    for (const s of document.querySelectorAll('.asof')) {
      // After the crumbs it goes on the line; on its own it starts it.
      s.textContent = (s.previousSibling ? '· archive' : 'Archive') + ' as of ' + fmt(u.at);
      if (!u.anchor || !here) continue;
      const a = document.createElement('a');
      a.href = new URL('../history.html#' + u.anchor, here).href;
      a.textContent = u.snapshot ? 'snapshot ' + u.snapshot + ' of ' + fmt(u.snapshot_at) : 'not in a snapshot yet';
      a.title = 'Latest change: ' + u.what + '. The history of the archive and its snapshots';
      s.append(' · ', a);
    }
  });
})();
`

// fullSnapshot is the newest snapshot of u that has every file and is still kept.
func fullSnapshot(u store.Update) (store.SnapshotRef, bool) {
	var best store.SnapshotRef
	found := false
	for _, s := range u.Snapshots {
		if !s.Partial && !s.Forgotten && (!found || s.At.After(best.At)) {
			best, found = s, true
		}
	}
	return best, found
}

// WriteHistory writes html/assets/updated.js and, for the archive itself
// (Track), html/history.html: every update, what it changed and the
// snapshots that hold it. Run it after a new update or snapshot.
func WriteHistory(ctx context.Context, db *store.Store, opts Options) error {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	stamp := map[string]any{"at": now().UTC().Format(time.RFC3339)}
	var updates []store.Update
	if opts.Track {
		var err error
		if updates, err = db.Updates(ctx, historyLimit+1); err != nil {
			return err
		}
	}
	// Backups are on once one was made, also before tgbup kept their history.
	_, backups, _ := db.Meta(ctx, "backup:host")
	backups = backups || anySnapshots(updates)
	if len(updates) > 0 {
		u := updates[0]
		stamp["anchor"], stamp["what"] = "u"+strconv.Itoa(u.N), u.Diff.Summary()
		if snap, ok := fullSnapshot(u); ok {
			stamp["snapshot"], stamp["snapshot_at"] = shortID(snap.ID), snap.At.UTC().Format(time.RFC3339)
		} else if !backups {
			delete(stamp, "anchor") // backups are off: nothing to say
		}
	}
	js, _ := json.Marshal(stamp)
	if err := writeFile(filepath.Join(opts.OutDir, "html", "assets", "updated.js"), func(w io.Writer) error {
		_, err := fmt.Fprintf(w, updatedJS, js)
		return err
	}); err != nil {
		return err
	}
	if !opts.Track {
		return nil
	}
	chats, err := db.RenderChats(ctx)
	if err != nil {
		return err
	}
	titles := map[int64]string{}
	for _, c := range chats {
		titles[c.ID] = orDefault(c.Title, strconv.FormatInt(c.ID, 10))
	}
	home, _ := os.UserHomeDir()
	page := historyPage{}
	if len(updates) > historyLimit {
		page.Older = updates[historyLimit].N
		updates = updates[:historyLimit]
	}
	for i, u := range updates {
		hu := historyUpdate{N: u.N, When: u.At.In(opts.Loc).Format("2006-01-02 15:04"), What: u.Diff.Summary()}
		if !u.Diff.First {
			hu.Chats = chatDeltas(u.Diff.Chats, titles)
		}
		if snap, ok := fullSnapshot(u); ok {
			hu.When = snap.At.In(opts.Loc).Format("2006-01-02 15:04")
		}
		for _, s := range u.Snapshots {
			place := tilde(backup.Display(s.Repo), home)
			short := shortID(s.ID)
			if !s.Partial && !s.Forgotten && !slices.Contains(hu.IDs, short) {
				hu.IDs = append(hu.IDs, short)
			}
			to := SnapshotCopyDir(opts.OutDir, short)
			hs := historySnap{Place: place, ID: short, Partial: s.Partial, Gone: s.Forgotten, Restore: RestoreCommand(place, short)}
			if _, err := os.Stat(filepath.Join(to, CopyDone)); err == nil {
				if rel, err := filepath.Rel(filepath.Join(opts.OutDir, "html"), filepath.Join(to, "html", "index.html")); err == nil {
					hs.Open = escapePath(filepath.ToSlash(rel))
				}
			}
			hu.Snapshots = append(hu.Snapshots, hs)
		}
		switch {
		case len(hu.IDs) > 0 || !backups:
		case i == 0:
			hu.NoSnapshot = "not in a snapshot yet"
		default:
			hu.NoSnapshot = "no snapshot of its own: the next one holds it"
		}
		page.Updates = append(page.Updates, hu)
	}
	return writeFile(filepath.Join(opts.OutDir, "html", "history.html"), func(w io.Writer) error {
		return historyHTML.Execute(w, page)
	})
}

// SnapshotCopyDir is where a snapshot of the archive in archiveDir is
// restored to be looked at: next to the archive, one folder each.
func SnapshotCopyDir(archiveDir, shortID string) string {
	archiveDir = filepath.Clean(archiveDir) // a trailing slash would put it inside the archive
	return filepath.Join(filepath.Dir(archiveDir), filepath.Base(archiveDir)+"-snapshots", shortID)
}

// RestoreCommand restores a copy of snapshot id from place (as shown to
// people, ~ for the home folder) into SnapshotCopyDir, for a shell.
func RestoreCommand(place, id string) string {
	return "tgbup restore --copy --from " + shellQuote(place) + " --snapshot " + id
}

// CopyDone marks a snapshot copy restored and drawn to the end.
const CopyDone = ".tgbup-copy-done"

func anySnapshots(us []store.Update) bool {
	for _, u := range us {
		if len(u.Snapshots) > 0 {
			return true
		}
	}
	return false
}

func shortID(id string) string { return id[:min(8, len(id))] }

// chatDeltas names the chats that changed most, up to 8.
func chatDeltas(d map[int64]int, titles map[int64]string) []string {
	ids := make([]int64, 0, len(d))
	for id := range d {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		return cmp.Or(cmp.Compare(abs(d[b]), abs(d[a])), cmp.Compare(titles[a], titles[b]))
	})
	var out []string
	for i, id := range ids {
		if i == 8 {
			out = append(out, fmt.Sprintf("and %d more", len(ids)-8))
			break
		}
		out = append(out, fmt.Sprintf("%s %+d", orDefault(titles[id], strconv.FormatInt(id, 10)), d[id]))
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func tilde(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// shellQuote quotes s for a shell unless it is plain; a leading ~ stays
// outside the quotes so the shell still expands it.
func shellQuote(s string) string {
	plain := func(s string) bool {
		for _, r := range s {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("/._-:~+@", r) {
				return false
			}
		}
		return true
	}
	if plain(s) {
		return s
	}
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return "~/'" + strings.ReplaceAll(rest, "'", `'\''`) + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
