// Package browse builds files/, a human-readable view of the downloaded media:
// files/<chat>/[<topic>/]<date time> <name>.<ext>, as hard links into media/.
// It is derived data and is rebuilt from scratch each time.
package browse

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

const (
	Dir     = "files"
	newDir  = ".files-new"
	oldDir  = ".files-old"
	maxName = 100
)

type Result struct {
	Linked int
	// Missing counts files the archive lists as downloaded but that are not on disk.
	Missing int
}

var kindNames = map[string]string{
	"photo": "photo", "video": "video", "voice": "voice", "video_note": "round", "audio": "audio",
	"document": "file", "sticker": "sticker", "animation": "gif",
}

// Build writes the view into a fresh directory and swaps it in, so files/ is
// never half-built and renamed chats leave nothing stale behind.
func Build(archiveDir string, files []store.DownloadedFile, loc *time.Location) (Result, error) {
	next, prev, final := filepath.Join(archiveDir, newDir), filepath.Join(archiveDir, oldDir), filepath.Join(archiveDir, Dir)
	for _, d := range []string{next, prev} {
		if err := os.RemoveAll(d); err != nil {
			return Result{}, err
		}
	}
	res, err := link(archiveDir, next, files, loc)
	if err != nil {
		return res, err
	}
	if err := os.Rename(final, prev); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	if err := os.Rename(next, final); err != nil {
		return res, err
	}
	return res, os.RemoveAll(prev)
}

func link(archiveDir, root string, files []store.DownloadedFile, loc *time.Location) (Result, error) {
	var res Result
	if err := os.MkdirAll(root, 0o755); err != nil {
		return res, err
	}
	chats := make([]FolderChat, 0, len(files))
	for _, f := range files {
		chats = append(chats, FolderChat{ID: f.ChatID, Title: f.ChatTitle, UpgradedTo: f.UpgradedTo, UpgradedTitle: f.UpgradedTitle})
	}
	folders := FolderNames(chats)
	used := map[string]bool{}
	for _, f := range files {
		dir := filepath.Join(root, folders[owner(f)])
		switch {
		case f.UpgradedTo != 0:
			dir = filepath.Join(dir, "before upgrade")
		case f.ChatType == store.ChatForum:
			topic := Clean(f.TopicTitle)
			if topic == "" {
				topic = "Topic " + strconv.Itoa(f.TopicID)
			}
			dir = filepath.Join(dir, topic)
		}
		ext := filepath.Ext(f.LocalPath)
		base := time.Unix(f.Date, 0).In(loc).Format("2006-01-02 15.04") + " " + fileName(f)
		dst := filepath.Join(dir, base+ext)
		for n := 2; used[dst]; n++ {
			dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, n, ext))
		}
		src := filepath.Join(archiveDir, f.LocalPath)
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			res.Missing++
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return res, err
		}
		if err := os.Link(src, dst); err != nil {
			return res, err
		}
		used[dst] = true
		res.Linked++
	}
	return res, nil
}

// owner is the chat whose folder a file goes to: a group upgraded to a
// supergroup files under the supergroup.
func owner(f store.DownloadedFile) int64 {
	if f.UpgradedTo != 0 {
		return f.UpgradedTo
	}
	return f.ChatID
}

// FolderChat is a chat as far as naming its folder is concerned.
type FolderChat struct {
	ID            int64
	Title         string
	UpgradedTo    int64
	UpgradedTitle string
}

// FolderNames names each chat's folder by its title; a group upgraded to a
// supergroup files under the supergroup, and chats sharing a title get their
// id appended so they never mix.
func FolderNames(chats []FolderChat) map[int64]string {
	titles := map[int64]string{}
	for _, c := range chats {
		id, title := c.ID, c.Title
		if c.UpgradedTo != 0 {
			id, title = c.UpgradedTo, c.UpgradedTitle
		}
		if t := Clean(title); t != "" {
			titles[id] = t
		} else {
			titles[id] = strconv.FormatInt(id, 10)
		}
	}
	seen := map[string]int{}
	for _, t := range titles {
		seen[strings.ToLower(t)]++
	}
	out := map[int64]string{}
	for id, t := range titles {
		if seen[strings.ToLower(t)] > 1 {
			t = fmt.Sprintf("%s (%d)", t, id)
		}
		out[id] = t
	}
	return out
}

func fileName(f store.DownloadedFile) string {
	if name := Clean(strings.TrimSuffix(f.FileName, filepath.Ext(f.FileName))); name != "" {
		return name
	}
	if name, ok := kindNames[f.Kind]; ok {
		return name
	}
	return "file"
}

// Clean turns a sender-controlled name into one safe path element: no
// separators or characters Windows and macOS reject, no control characters,
// no leading dots (hidden files, ".."), at most maxName characters.
func Clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), !unicode.IsPrint(r) && r != '‍':
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, ". ")
	if r := []rune(s); len(r) > maxName {
		s = string(r[:maxName])
	}
	return strings.TrimRight(s, ". ")
}
