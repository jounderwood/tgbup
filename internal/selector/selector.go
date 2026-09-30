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
	AllChats       = "All chats"
	OutsideFolders = "No folder"
	ArchiveGroup   = "Archived"
)

type Group struct {
	Title string
	Chats []tgclient.Dialog
}

// Groups lists all chats, every Telegram folder, then non-archived chats in no
// folder, then archived chats. A chat in several folders appears in each of them.
func Groups(dialogs []tgclient.Dialog, folders []tgclient.Folder) []Group {
	out := []Group{{Title: AllChats, Chats: dialogs}}
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
			return nil, fmt.Errorf("invalid number or range %q (valid: 1-%d)", part, n)
		}
		for i := from; i <= to; i++ {
			seen[i-1] = true
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("no numbers given")
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	slices.Sort(out)
	return out, nil
}

func Search(dialogs []tgclient.Dialog, q string) []tgclient.Dialog {
	q = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	var out []tgclient.Dialog
	for _, d := range dialogs {
		if strings.Contains(strings.ToLower(d.Title), q) || (d.Username != "" && strings.Contains(strings.ToLower(d.Username), q)) {
			out = append(out, d)
		}
	}
	return out
}

// State is the whole setup choice: selected chats plus excluded forum topics.
type State struct {
	Chats    Selection
	Excluded map[int64]map[int]bool
}

func NewState() *State { return &State{Chats: Selection{}, Excluded: map[int64]map[int]bool{}} }

func (s *State) TopicArchived(chat int64, topic int) bool {
	return s.Chats[chat] && !s.Excluded[chat][topic]
}

// ExcludedFor returns the sorted excluded topics of a selected chat.
func (s *State) ExcludedFor(chat int64) []int {
	if !s.Chats[chat] || len(s.Excluded[chat]) == 0 {
		return nil
	}
	out := make([]int, 0, len(s.Excluded[chat]))
	for id := range s.Excluded[chat] {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ToggleTopics flips the archive mark of topics all[idx...] of a forum.
func (s *State) ToggleTopics(chat int64, all []int, idx []int) {
	archived := s.archivedTopics(chat, all)
	for _, i := range idx {
		archived[all[i]] = !archived[all[i]]
	}
	s.setTopics(chat, all, archived)
}

func (s *State) SetAllTopics(chat int64, all []int, on bool) {
	archived := map[int]bool{}
	for _, id := range all {
		archived[id] = on
	}
	s.setTopics(chat, all, archived)
}

func (s *State) archivedTopics(chat int64, all []int) map[int]bool {
	out := map[int]bool{}
	for _, id := range all {
		out[id] = s.TopicArchived(chat, id)
	}
	return out
}

// setTopics keeps the forum selected exactly while at least one topic is archived.
// With no known topics there is nothing to decide, so the selection stays as is.
func (s *State) setTopics(chat int64, all []int, archived map[int]bool) {
	if len(all) == 0 {
		return
	}
	excluded := map[int]bool{}
	for _, id := range all {
		if !archived[id] {
			excluded[id] = true
		}
	}
	if len(excluded) == len(all) {
		delete(s.Chats, chat)
		delete(s.Excluded, chat)
		return
	}
	s.Chats[chat] = true
	if len(excluded) == 0 {
		delete(s.Excluded, chat)
	} else {
		s.Excluded[chat] = excluded
	}
}

// Initial marks what the current config selects, so setup starts from it.
// A folder that no longer exists in Telegram only costs its own chats (with a
// warning), not the rest of the selection.
func Initial(dialogs []tgclient.Dialog, folders []tgclient.Folder, sel config.Targets) (*State, []string) {
	var warnings []string
	resolvable := sel
	resolvable.Folders = nil
	for _, name := range sel.Folders {
		if slices.ContainsFunc(folders, func(f tgclient.Folder) bool { return f.Title == name }) {
			resolvable.Folders = append(resolvable.Folders, name)
		} else {
			warnings = append(warnings, fmt.Sprintf("folder %q not found in Telegram, its chats are not preselected", name))
		}
	}
	targets, more, err := tgclient.SelectTargets(dialogs, folders, resolvable)
	st := NewState()
	if err != nil {
		return st, append(warnings, err.Error())
	}
	warnings = append(warnings, more...)
	for _, t := range targets {
		st.Chats[t.Chat.ID] = true
		if len(t.ExcludedTopics) > 0 {
			st.Excluded[t.Chat.ID] = map[int]bool{}
			for _, id := range t.ExcludedTopics {
				st.Excluded[t.Chat.ID][id] = true
			}
		}
	}
	return st, warnings
}
