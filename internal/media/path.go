package media

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/joeunderwood/tgbup/internal/names"
	"github.com/joeunderwood/tgbup/internal/store"
)

var safeExt = regexp.MustCompile(`^\.[a-zA-Z0-9]{1,10}$`)

var mimeExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a",
	"application/pdf": ".pdf", "application/zip": ".zip", "application/x-tgsticker": ".tgs",
}

var kindExt = map[string]string{
	"photo": ".jpg", "voice": ".ogg", "video_note": ".mp4", "video": ".mp4", "animation": ".mp4", "sticker": ".webp",
}

// kindNames name a file that has no name of its own and came from a chat without a title.
var kindNames = map[string]string{
	"photo": "photo", "video": "video", "voice": "voice", "video_note": "round", "audio": "audio",
	"document": "file", "sticker": "sticker", "animation": "gif",
}

// kindDir is the folder of a kind of file inside the year: photo, video,
// file, voice, round, audio, sticker, gif.
func kindDir(kind string) string {
	if d, ok := kindNames[kind]; ok {
		return d
	}
	return "file"
}

// runnable extensions get .txt appended: a double click in Finder must not run
// a file someone sent.
var runnable = map[string]bool{
	".app": true, ".command": true, ".tool": true, ".terminal": true, ".sh": true, ".bash": true, ".zsh": true,
	".csh": true, ".ksh": true, ".fish": true, ".py": true, ".pl": true, ".rb": true, ".php": true,
	".pkg": true, ".mpkg": true, ".dmg": true, ".jar": true, ".scpt": true, ".scptd": true, ".applescript": true,
	".workflow": true, ".action": true, ".webloc": true, ".inetloc": true, ".fileloc": true, ".url": true,
	".mobileconfig": true, ".prefpane": true, ".kext": true, ".plugin": true, ".osax": true, ".bundle": true,
	".dylib": true, ".so": true, ".exe": true, ".msi": true, ".bat": true, ".cmd": true, ".ps1": true, ".vbs": true,
	".desktop": true, ".appimage": true, ".run": true,
	// Disk images mount like .dmg; location files open what they point to.
	".img": true, ".iso": true, ".cdr": true, ".sparseimage": true, ".sparsebundle": true, ".toast": true,
	".afploc": true, ".ftploc": true, ".mailloc": true, ".newsloc": true, ".xip": true, ".shortcut": true,
	".webarchive": true,
}

// maxFileName is the file system's limit for one name, in bytes; the name
// must leave room for the download's temporary name, PartPath.
const maxFileName = 255 - len(partPrefix) - len(partSuffix)

const (
	partPrefix = "."
	partSuffix = ".part"
)

// PartPath is where a file is downloaded before it gets its name. The leading
// dot keeps it apart from every real name, which starts with the date.
func PartPath(final string) string {
	return filepath.Join(filepath.Dir(final), partPrefix+filepath.Base(final)+partSuffix)
}

// RelPath is the n-th candidate name of a file (n > 1 adds " (n)"):
// media/<year>/<kind>/<date>_<time>_<name><ext>. The name is the sender's file name
// as it was, made safe, or the chat's title when the file has none; a voice
// message adds who said it: "Семья_Катерина".
func RelPath(j store.MediaJob, loc *time.Location, n int) string {
	t := time.Unix(j.Date, 0).In(loc)
	prefix := t.Format("2006-01-02_150405") + "_"
	ext := Ext(j.FileName, j.Mime, j.Kind)
	suffix := ""
	if n > 1 {
		suffix = fmt.Sprintf(" (%d)", n)
	}
	room := maxFileName - len(prefix) - len(suffix) - len(ext)
	name := names.CleanBytes(baseName(j.FileName), room)
	if name == "" {
		title := j.ChatTitle
		if j.Kind == "voice" && j.Sender != "" && j.Sender != title {
			title = strings.TrimPrefix(title+"_"+j.Sender, "_")
		}
		name = names.CleanBytes(title, room)
	}
	if name == "" {
		name = kindNames[j.Kind]
	}
	if name == "" {
		name = "file"
	}
	return filepath.Join("media", t.Format("2006"), kindDir(j.Kind), prefix+name+suffix+ext)
}

// baseName is the file name without the extension Ext takes from it.
func baseName(fileName string) string {
	if e := filepath.Ext(fileName); safeExt.MatchString(e) {
		return strings.TrimSuffix(fileName, e)
	}
	return fileName
}

// Ext is the file's extension as the sender named it when it is plain letters
// and digits, else one from the MIME type or kind; runnable ones get .txt.
func Ext(fileName, mimeType, kind string) string {
	if e := filepath.Ext(fileName); safeExt.MatchString(e) {
		if runnable[strings.ToLower(e)] {
			return e + ".txt"
		}
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

// PathKey is a path as APFS compares names: case (fully folded, so "ß" is
// "ss") and Unicode normalization do not matter, so "Фото.jpg" and
// "фото.jpg" are one name.
func PathKey(rel string) string {
	return norm.NFD.String(cases.Fold().String(norm.NFD.String(rel)))
}
