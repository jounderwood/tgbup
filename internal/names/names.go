// Package names turns sender-controlled names (chat titles, file names) into
// safe file and folder names.
package names

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const maxName = 100

// FolderChat is a chat as far as naming its folder is concerned.
type FolderChat struct {
	ID            int64
	Title         string
	UpgradedTo    int64
	UpgradedTitle string
}

// Key is a name as the disk compares names (macOS: case and Unicode
// normalization do not matter), for telling whether two names are one.
func Key(s string) string { return norm.NFD.String(cases.Fold().String(norm.NFD.String(s))) }

// siteFiles are the names html/ and md/ use themselves: a chat named after
// one gets its id appended, or its folder would take the file's place.
var siteFiles = map[string]bool{"index.html": true, "index.md": true, "media.html": true, "history.html": true,
	"assets": true, "search": true, "media": true}

// ThreadFile says name is taken in a chat's folder, so a topic's folder must
// not use it: the index, the calendar, the page list and the pages.
func ThreadFile(name string) bool {
	k := Key(name)
	switch k {
	case "index.html", "index.md", "dates.html", "dates.md", "nav.js":
		return true
	}
	if rest, ok := strings.CutPrefix(k, "p"); ok {
		rest = strings.TrimSuffix(strings.TrimSuffix(rest, ".html"), ".md")
		if _, err := strconv.Atoi(rest); err == nil {
			return true
		}
	}
	return false
}

// FolderNames names each chat's folder by its title; a group upgraded to a
// supergroup files under the supergroup, and chats whose titles the disk
// would see as one (or as one of the site's own files) get their id appended
// so they never mix.
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
		seen[Key(t)]++
	}
	out := map[int64]string{}
	for id, t := range titles {
		if seen[Key(t)] > 1 || siteFiles[Key(t)] {
			t = fmt.Sprintf("%s (%d)", t, id)
		}
		out[id] = t
	}
	return out
}

// Clean turns a sender-controlled name into one safe path element: no
// separators or characters Windows and macOS reject, no control characters,
// no leading dots (hidden files, ".."), at most maxName characters.
func Clean(s string) string {
	s = clean(s)
	if r := []rune(s); len(r) > maxName {
		s = string(r[:maxName])
	}
	return strings.TrimRight(s, ". ")
}

// CleanBytes is Clean with a limit in bytes instead of characters, for names
// that must fit the file system's 255 bytes with a prefix and an extension.
func CleanBytes(s string, maxBytes int) string {
	s = clean(s)
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimRight(s, ". ")
}

func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), !unicode.IsPrint(r) && r != '\u200d':
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimLeft(s, ". ")
}
