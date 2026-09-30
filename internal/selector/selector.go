// Package selector holds the chat selection logic behind `tgbup setup`:
// grouping dialogs by Telegram folder, parsing number input and search.
package selector

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

const (
	OutsideFolders = "Вне папок"
	ArchiveGroup   = "Архив"
)

type Group struct {
	Title string
	Chats []tgclient.Dialog
}

// Groups lists every Telegram folder, then non-archived chats in no folder,
// then archived chats. A chat in several folders appears in each of them.
func Groups(dialogs []tgclient.Dialog, folders []tgclient.Folder) []Group {
	var out []Group
	inFolder := map[int64]bool{}
	for _, f := range folders {
		g := Group{Title: f.Title}
		for _, d := range dialogs {
			if f.Matches(d) {
				g.Chats = append(g.Chats, d)
				inFolder[d.ID] = true
			}
		}
		out = append(out, g)
	}
	var outside, archived []tgclient.Dialog
	for _, d := range dialogs {
		switch {
		case d.Archived:
			archived = append(archived, d)
		case !inFolder[d.ID]:
			outside = append(outside, d)
		}
	}
	return append(out, Group{Title: OutsideFolders, Chats: outside}, Group{Title: ArchiveGroup, Chats: archived})
}

// Selection is keyed by chat id, so a chat looks the same in every group it is in.
type Selection map[int64]bool

func (s Selection) Count(chats []tgclient.Dialog) int {
	n := 0
	for _, c := range chats {
		if s[c.ID] {
			n++
		}
	}
	return n
}

func (s Selection) Toggle(chats []tgclient.Dialog, idx []int) {
	for _, i := range idx {
		id := chats[i].ID
		if s[id] {
			delete(s, id)
		} else {
			s[id] = true
		}
	}
}

func (s Selection) SetAll(chats []tgclient.Dialog, on bool) {
	for _, c := range chats {
		if on {
			s[c.ID] = true
		} else {
			delete(s, c.ID)
		}
	}
}

func (s Selection) IDs() []int64 {
	out := make([]int64, 0, len(s))
	for id := range s {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ParseIndices turns "1,3,5-7" (1-based, as shown on screen) into sorted unique 0-based indices.
func ParseIndices(input string, n int) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, hi = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		from, err1 := strconv.Atoi(lo)
		to, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || from < 1 || to > n || from > to {
			return nil, fmt.Errorf("неверный номер или диапазон %q (доступно 1–%d)", part, n)
		}
		for i := from; i <= to; i++ {
			seen[i-1] = true
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("не указано ни одного номера")
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	slices.Sort(out)
	return out, nil
}

func Search(dialogs []tgclient.Dialog, q string) []tgclient.Dialog {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []tgclient.Dialog
	for _, d := range dialogs {
		if strings.Contains(strings.ToLower(d.Title), q) || (d.Username != "" && strings.Contains(strings.ToLower(d.Username), q)) {
			out = append(out, d)
		}
	}
	return out
}

// Initial marks what the current config selects, so setup starts from it.
// A folder that no longer exists in Telegram only costs its own chats (with a
// warning), not the rest of the selection.
func Initial(dialogs []tgclient.Dialog, folders []tgclient.Folder, sel config.Targets) (Selection, []string) {
	var warnings []string
	resolvable := sel
	resolvable.Folders = nil
	for _, name := range sel.Folders {
		if slices.ContainsFunc(folders, func(f tgclient.Folder) bool { return f.Title == name }) {
			resolvable.Folders = append(resolvable.Folders, name)
		} else {
			warnings = append(warnings, fmt.Sprintf("папка %q не найдена в Telegram, её чаты не отмечены", name))
		}
	}
	targets, more, err := tgclient.SelectTargets(dialogs, folders, resolvable)
	if err != nil {
		return Selection{}, append(warnings, err.Error())
	}
	warnings = append(warnings, more...)
	s := Selection{}
	for _, t := range targets {
		s[t.Chat.ID] = true
	}
	return s, warnings
}
