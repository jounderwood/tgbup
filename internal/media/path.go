package media

import (
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.com/joeunderwood/tgbp/internal/store"
)

var safeExt = regexp.MustCompile(`^\.[a-z0-9]{1,10}$`)

var mimeExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a",
	"application/pdf": ".pdf", "application/zip": ".zip", "application/x-tgsticker": ".tgs",
}

var kindExt = map[string]string{
	"photo": ".jpg", "voice": ".ogg", "video_note": ".mp4", "video": ".mp4", "animation": ".mp4", "sticker": ".webp",
}

// RelPath builds the on-disk path only from the Telegram id and a whitelisted
// extension, never from the sender-controlled file name.
func RelPath(j store.MediaJob) string {
	_, id, _ := strings.Cut(j.Key, ":")
	return filepath.Join("media", j.Kind, id+Ext(j.FileName, j.Mime, j.Kind))
}

func Ext(fileName, mimeType, kind string) string {
	if e := strings.ToLower(filepath.Ext(fileName)); safeExt.MatchString(e) {
		return e
	}
	if e, ok := mimeExt[mimeType]; ok {
		return e
	}
	if e, ok := kindExt[kind]; ok {
		return e
	}
	return ".bin"
}
