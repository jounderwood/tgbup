package tgclient

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/config"
	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
)

type Dialog struct {
	ID         int64
	Peer       tg.InputPeerClass
	Type       string
	Title      string
	Username   string
	Contact    bool
	Bot        bool
	Archived   bool
	TopMessage int
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
	// ExcludedTopics lists forum topics whose messages are not archived.
	ExcludedTopics []int
	// TopMessage is the dialog's latest message id when targets were resolved;
	// 0 when unknown (e.g. a pre-migration group).
	TopMessage int
}

func (t Target) IsForum() bool { return t.Chat.Type == store.ChatForum }

func (f Folder) Matches(d Dialog) bool {
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
			if f.Matches(d) {
				in = append(in, f.Title)
			}
		}
		if len(in) == 0 && !include[d.ID] {
			continue
		}
		out = append(out, Target{
			Chat:           store.Chat{ID: d.ID, Type: d.Type, Title: d.Title, Username: d.Username, Folders: in},
			Peer:           d.Peer,
			TopMessage:     d.TopMessage,
			ExcludedTopics: sel.ExcludeTopics[d.ID],
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

// Dialogs lists every dialog once. Without folder_id Telegram returns the archive
// too, so the archive folder is fetched separately only to mark archived chats.
func (a *API) Dialogs(ctx context.Context) ([]Dialog, error) {
	all, err := a.listDialogs(ctx, a.dialogsQuery(), false)
	if err != nil {
		return nil, fmt.Errorf("list dialogs: %w", err)
	}
	archived, err := a.listDialogs(ctx, a.dialogsQuery().FolderID(1), true)
	if err != nil {
		return nil, fmt.Errorf("list archived dialogs: %w", err)
	}
	return mergeDialogs(all, archived), nil
}

func (a *API) dialogsQuery() *dialogs.GetDialogsQueryBuilder {
	return query.NewQuery(a.raw).GetDialogs().BatchSize(100)
}

func (a *API) listDialogs(ctx context.Context, q *dialogs.GetDialogsQueryBuilder, archived bool) ([]Dialog, error) {
	var out []Dialog
	err := q.ForEach(ctx, func(ctx context.Context, e dialogs.Elem) error {
		if d, ok := dialogFromElem(e, archived); ok {
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

func mergeDialogs(all, archived []Dialog) []Dialog {
	isArchived := map[int64]bool{}
	for _, d := range archived {
		isArchived[d.ID] = true
	}
	seen := map[int64]bool{}
	var out []Dialog
	for _, d := range append(slices.Clone(all), archived...) {
		if seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		d.Archived = isArchived[d.ID]
		out = append(out, d)
	}
	return out
}

func dialogFromElem(e dialogs.Elem, archived bool) (Dialog, bool) {
	d := Dialog{Peer: e.Peer, Archived: archived}
	if dlg, ok := e.Dialog.(*tg.Dialog); ok {
		d.TopMessage = dlg.TopMessage
	}
	switch p := e.Peer.(type) {
	case *tg.InputPeerUser:
		u, ok := e.Entities.User(p.UserID)
		if !ok {
			return Dialog{}, false
		}
		d.ID, d.Type, d.Username, d.Contact, d.Bot = p.UserID, store.ChatPrivate, u.Username, u.Contact, u.Bot
		d.Title = strings.TrimSpace(u.FirstName + " " + u.LastName)
		if u.Deleted && d.Title == "" {
			d.Title = "Deleted Account"
		}
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

// Folders maps folder peers to chat ids; Saved Messages comes back as InputPeerSelf,
// which only selfID can resolve.
func (a *API) Folders(ctx context.Context, selfID int64) ([]Folder, error) {
	res, err := a.raw.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("get dialog filters: %w", err)
	}
	var out []Folder
	for _, fc := range res.Filters {
		switch f := fc.(type) {
		case *tg.DialogFilter:
			out = append(out, Folder{
				Title: f.Title.Text, Pinned: peerIDs(f.PinnedPeers, selfID), Include: peerIDs(f.IncludePeers, selfID), Exclude: peerIDs(f.ExcludePeers, selfID),
				Contacts: f.Contacts, NonContacts: f.NonContacts, Groups: f.Groups, Broadcasts: f.Broadcasts, Bots: f.Bots,
				ExcludeArchived: f.ExcludeArchived,
			})
		case *tg.DialogFilterChatlist:
			out = append(out, Folder{Title: f.Title.Text, Pinned: peerIDs(f.PinnedPeers, selfID), Include: peerIDs(f.IncludePeers, selfID)})
		}
	}
	return out, nil
}

func peerIDs(ps []tg.InputPeerClass, selfID int64) []int64 {
	var out []int64
	for _, p := range ps {
		if _, self := p.(*tg.InputPeerSelf); self {
			out = append(out, selfID)
			continue
		}
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

// TopicExclusionNotes reports exclude_topics keys that have no effect. They
// lose nothing, so unlike an unresolved include they are only worth a warning.
func TopicExclusionNotes(targets []Target, sel config.Targets) []string {
	byID := map[int64]Target{}
	for _, t := range targets {
		byID[t.Chat.ID] = t
	}
	var notes []string
	for _, id := range slices.Sorted(maps.Keys(sel.ExcludeTopics)) {
		t, ok := byID[id]
		switch {
		case !ok:
			notes = append(notes, fmt.Sprintf("exclude_topics %d: chat is not selected", id))
		case !t.IsForum():
			notes = append(notes, fmt.Sprintf("exclude_topics %d: chat is not a forum", id))
		}
	}
	return notes
}

// NameSelf titles the user's own dialog "Saved Messages", as Telegram apps do,
// instead of the user's name.
func NameSelf(ds []Dialog, selfID int64) {
	for i := range ds {
		if ds[i].ID == selfID {
			ds[i].Title = "Saved Messages"
		}
	}
}
