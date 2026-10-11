package media

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
)

// ForgetStickers: stickers are kept as their emoji, not as files. An archive
// from before has them as files: the files go (and a download left half-way),
// and their messages get the emoji from the message as Telegram gave it.
// Snapshots made before keep the files. It needs no Telegram, and runs before
// a sync, so the sync reads the messages again as they are kept now.
func ForgetStickers(ctx context.Context, st *store.Store, archiveDir string, log *slog.Logger) error {
	files, err := st.StickerFiles(ctx)
	if err != nil || len(files) == 0 {
		return err
	}
	removed, freed := 0, int64(0)
	for _, f := range files {
		if f.Path == "" || !filepath.IsLocal(f.Path) {
			continue
		}
		p := filepath.Join(archiveDir, filepath.FromSlash(f.Path))
		for _, q := range []string{p, PartPath(p)} {
			switch err := os.Remove(q); {
			case err == nil && q == p:
				removed++
				freed += f.Size
			case err == nil, errors.Is(err, fs.ErrNotExist):
			default:
				// It stays; the archive no longer shows it.
				log.Warn("a sticker's file could not be removed", "file", f.Path, "err", err)
			}
		}
		removeStickerDir(filepath.Dir(p))
	}
	if _, err := st.ForgetStickers(ctx, stickerMeta); err != nil {
		return err
	}
	log.Info("stickers are kept as their emoji now: their files went", "files", removed, "MB", freed>>20)
	return nil
}

// stickerMeta is the meta of a sticker message as Telegram gave it (raw);
// false when raw is no sticker.
func stickerMeta(raw []byte) (string, bool) {
	mj, err := convert.MetaJSON(raw)
	if err != nil {
		return "", false
	}
	var m convert.Meta
	if json.Unmarshal([]byte(mj), &m) != nil || m.Sticker == nil {
		return "", false
	}
	return mj, true
}

// removeStickerDir removes media/<year>/sticker once it is empty: that
// folder itself, never a link to one, nor any other folder.
func removeStickerDir(dir string) {
	if filepath.Base(dir) != "sticker" {
		return
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return
	}
	_ = os.Remove(dir) // only if empty
}
