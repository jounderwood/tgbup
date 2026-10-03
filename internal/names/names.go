// Package names turns sender-controlled names (chat titles, file names) into
// safe file and folder names.
package names

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxName = 100

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
