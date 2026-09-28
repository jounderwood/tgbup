package tgclient

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbp/internal/config"
	"gitlab.com/joeunderwood/tgbp/internal/convert"
	"gitlab.com/joeunderwood/tgbp/internal/store"
)

type Dialog struct {
	ID       int64
	Peer     tg.InputPeerClass
	Type     string
	Title    string
	Username string
	Contact  bool
	Bot      bool
	Archived bool
}

// Folder mirrors a Telegram dialog filter. exclude_muted/exclude_read are ignored on
// purpose: they change constantly and would make the archived set flap.
type Folder struct {
	Title           string
	Pinned          []int64
	Include         []int64
	Exclude         []int64
	Contacts        bool
	NonContacts     bool
	Groups          bool
	Broadcasts      bool
	Bots            bool
	ExcludeArchived bool
}

type Target struct {
	Chat store.Chat
	Peer tg.InputPeerClass
}

func (t Target) IsForum() bool { return t.Chat.Type == store.ChatForum }

func (f Folder) matches(d Dialog) bool {
	if slices.Contains(f.Exclude, d.ID) {
		return false
	}
	if slices.Contains(f.Pinned, d.ID) || slices.Contains(f.Include, d.ID) {
		return true
	}
	if f.ExcludeArchived && d.Archived {
		return false
	}
	switch d.Type {
	case store.ChatPrivate:
		if d.Bot {
			return f.Bots
		}
		if d.Contact {
			return f.Contacts
		}
		return f.NonContacts
	case store.ChatGroup, store.ChatSupergroup, store.ChatForum:
		return f.Groups
	case store.ChatChannel:
		return f.Broadcasts
	}
	return false
}

func SelectTargets(dialogs []Dialog, folders []Folder, sel config.Targets) ([]Target, []string, error) {
	byTitle := map[string]Folder{}
	for _, f := range folders {
		byTitle[f.Title] = f
	}
	var (
		chosen []Folder
		errs   []error
	)
	for _, name := range sel.Folders {
		f, ok := byTitle[name]
		if !ok {
			errs = append(errs, fmt.Errorf("telegram folder %q not found", name))
			continue
		}
		chosen = append(chosen, f)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}

	find := func(ref config.PeerRef) (int64, bool) {
		for _, d := range dialogs {
			if id, ok := ref.ID(); ok && d.ID == id {
				return d.ID, true
			}
			if u, ok := ref.Username(); ok && d.Username != "" && strings.EqualFold(d.Username, u) {
				return d.ID, true
			}
		}
		return 0, false
	}
	var warnings []string
	include, exclude := map[int64]bool{}, map[int64]bool{}
	for _, r := range sel.Include {
		id, ok := find(r)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("include %s: chat not found among your dialogs", string(r)))
			continue
		}
		include[id] = true
	}
	for _, r := range sel.Exclude {
		if id, ok := find(r); ok {
			exclude[id] = true
		}
	}

	var out []Target
	for _, d := range dialogs {
		if exclude[d.ID] {
			continue
		}
		var in []string
		for _, f := range chosen {
			if f.matches(d) {
				in = append(in, f.Title)
			}
		}
		if len(in) == 0 && !include[d.ID] {
			continue
		}
		out = append(out, Target{
			Chat: store.Chat{ID: d.ID, Type: d.Type, Title: d.Title, Username: d.Username, Folders: in},
			Peer: d.Peer,
		})
	}
	slices.SortFunc(out, func(a, b Target) int {
		switch {
		case a.Chat.ID < b.Chat.ID:
			return -1
		case a.Chat.ID > b.Chat.ID:
			return 1
		}
		return 0
	})
	return out, warnings, nil
}

func (a *API) Dialogs(ctx context.Context) ([]Dialog, error) {
	var out []Dialog
	for _, folderID := range []int{0, 1} {
		err := query.NewQuery(a.raw).GetDialogs().FolderID(folderID).BatchSize(100).
			ForEach(ctx, func(ctx context.Context, e dialogs.Elem) error {
				if d, ok := dialogFromElem(e, folderID == 1); ok {
					out = append(out, d)
				}
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("list dialogs (folder %d): %w", folderID, err)
		}
	}
	return out, nil
}

func dialogFromElem(e dialogs.Elem, archived bool) (Dialog, bool) {
	d := Dialog{Peer: e.Peer, Archived: archived}
	switch p := e.Peer.(type) {
	case *tg.InputPeerUser:
		u, ok := e.Entities.User(p.UserID)
		if !ok || u.Deleted {
			return Dialog{}, false
		}
		d.ID, d.Type, d.Username, d.Contact, d.Bot = p.UserID, store.ChatPrivate, u.Username, u.Contact, u.Bot
		d.Title = strings.TrimSpace(u.FirstName + " " + u.LastName)
	case *tg.InputPeerChat:
		c, ok := e.Entities.Chat(p.ChatID)
		if !ok || c.Deactivated {
			return Dialog{}, false
		}
		d.ID, d.Type, d.Title = convert.ChatID(p.ChatID), store.ChatGroup, c.Title
	case *tg.InputPeerChannel:
		c, ok := e.Entities.Channel(p.ChannelID)
		if !ok {
			return Dialog{}, false
		}
		d.ID, d.Title, d.Username = convert.ChannelID(p.ChannelID), c.Title, c.Username
		switch {
		case c.Forum:
			d.Type = store.ChatForum
		case c.Megagroup:
			d.Type = store.ChatSupergroup
		default:
			d.Type = store.ChatChannel
		}
	default:
		return Dialog{}, false
	}
	return d, true
}

func (a *API) Folders(ctx context.Context) ([]Folder, error) {
	res, err := a.raw.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("get dialog filters: %w", err)
	}
	var out []Folder
	for _, fc := range res.Filters {
		switch f := fc.(type) {
		case *tg.DialogFilter:
			out = append(out, Folder{
				Title: f.Title.Text, Pinned: peerIDs(f.PinnedPeers), Include: peerIDs(f.IncludePeers), Exclude: peerIDs(f.ExcludePeers),
				Contacts: f.Contacts, NonContacts: f.NonContacts, Groups: f.Groups, Broadcasts: f.Broadcasts, Bots: f.Bots,
				ExcludeArchived: f.ExcludeArchived,
			})
		case *tg.DialogFilterChatlist:
			out = append(out, Folder{Title: f.Title.Text, Pinned: peerIDs(f.PinnedPeers), Include: peerIDs(f.IncludePeers)})
		}
	}
	return out, nil
}

func peerIDs(ps []tg.InputPeerClass) []int64 {
	var out []int64
	for _, p := range ps {
		if id, ok := convert.InputPeerID(p); ok {
			out = append(out, id)
		}
	}
	return out
}

// AddMigrated links supergroups to the basic group they were upgraded from and adds
// that old group as a target, so its pre-migration history is archived too.
func (a *API) AddMigrated(ctx context.Context, targets []Target) ([]Target, []string) {
	have := map[int64]bool{}
	for _, t := range targets {
		have[t.Chat.ID] = true
	}
	var warnings []string
	out := slices.Clone(targets)
	for i, t := range targets {
		ch, ok := t.Peer.(*tg.InputPeerChannel)
		if !ok || (t.Chat.Type != store.ChatSupergroup && t.Chat.Type != store.ChatForum) {
			continue
		}
		full, err := a.raw.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("chat %d: cannot check migration: %v", t.Chat.ID, err))
			continue
		}
		cf, ok := full.FullChat.(*tg.ChannelFull)
		if !ok {
			continue
		}
		oldRaw, ok := cf.GetMigratedFromChatID()
		if !ok {
			continue
		}
		old := convert.ChatID(oldRaw)
		out[i].Chat.MigratedFromID = old
		if have[old] {
			continue
		}
		have[old] = true
		out = append(out, Target{
			Chat: store.Chat{ID: old, Type: store.ChatGroup, Title: t.Chat.Title, Folders: t.Chat.Folders},
			Peer: &tg.InputPeerChat{ChatID: oldRaw},
		})
	}
	return out, warnings
}
