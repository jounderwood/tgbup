package syncer

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

const defaultFeedRequests = 500

// feedBox is a feed to read: the account's own (id 0, no peer) or a
// channel's or supergroup's.
type feedBox struct {
	id   int64
	peer tg.InputPeerClass
}

// feed reads Telegram's change feeds since the last run, before the chats
// are synced: the account's own, for private chats and basic groups, and
// each channel's and supergroup's. Edits, reactions and deletions of
// archived messages reach the archive whatever their age, for a request or
// so a feed; new messages are left to the forward sync. A channel's feed is
// followed from where the chat's history last said it stood (Run). One that
// Telegram lost track of (too long since the last run), or will not go on
// with from where it stood, goes on from now: the recent window and the
// audit cover the gap. The least recently read go first, within
// FeedRequests requests and the run's time; what is left, or fails, waits
// for the next run. Like the audit, it never fails the run.
func (s *Syncer) feed(ctx context.Context, targets []tgclient.Target, deadline time.Time, r *Report) {
	budget := cmp.Or(s.FeedRequests, defaultFeedRequests)
	if s.Feeds == nil || budget < 0 {
		return
	}
	followed, err := s.Store.Feeds(ctx)
	if err != nil {
		s.Log.Warn("change feeds", "err", err)
		return
	}
	byID := map[int64]tgclient.Target{}
	var boxes []feedBox
	common := false
	for _, t := range targets {
		byID[t.Chat.ID] = t
		if _, ok := t.Peer.(*tg.InputPeerChannel); !ok {
			common = true
		} else if _, ok := followed[t.Chat.ID]; ok {
			boxes = append(boxes, feedBox{id: t.Chat.ID, peer: t.Peer})
		}
	}
	if common {
		boxes = append(boxes, feedBox{id: 0})
	}
	if len(boxes) == 0 {
		return
	}
	// Never read first, then the least recently read: a run that stops
	// early leaves the others first for the next one.
	slices.SortStableFunc(boxes, func(a, b feedBox) int { return cmp.Compare(followed[a.id].ReadAt, followed[b.id].ReadAt) })
	spend := func() bool {
		if budget <= 0 || !s.Now().Before(deadline) {
			return false
		}
		budget--
		return true
	}

	was, read, failed := *r, 0, 0
feeds:
	for i, b := range boxes {
		s.feedProgress(i, len(boxes))
		f, ok := followed[b.id]
		err := s.readFeed(ctx, b, f, ok, byID, spend, r)
		switch {
		case err == nil:
			read, failed = read+1, 0
		case errors.Is(err, errBudget):
			s.Log.Info("change feeds: the run's share of requests or time is spent; the next run goes on", "left", len(boxes)-i)
			break feeds
		case tgclient.Classify(err) == tgclient.KindInaccessible: // the chat's sync reports it
		case stopsFeeds(ctx, err):
			s.Log.Warn("reading Telegram's change feeds stopped; the next run goes on", "feed", feedName(b.id), "err", err)
			break feeds
		default:
			s.Log.Warn("reading a change feed stopped; the next run tries again", "feed", feedName(b.id), "err", err)
			// The same failure feed after feed is the account's or the
			// connection's, not a chat's: the others would fail too.
			if failed++; failed == 3 {
				s.Log.Warn("reading Telegram's change feeds stopped after failing three in a row; the next run goes on")
				break feeds
			}
		}
	}
	s.feedProgress(len(boxes), len(boxes))
	s.Log.Info("from Telegram's change feeds", "feeds", read,
		"edited", r.Edited-was.Edited, "updated", r.Updated-was.Updated, "deleted", r.Deleted-was.Deleted)
}

// readFeed reads a feed to its end, noting where it got to after each page
// once the page is in the archive.
func (s *Syncer) readFeed(ctx context.Context, b feedBox, f store.Feed, followed bool, byID map[int64]tgclient.Target,
	spend func() bool, r *Report) error {
	if !followed {
		return s.restartFeed(ctx, b, spend)
	}
	st := tgclient.FeedState{Pts: f.Pts, Qts: f.Qts, Date: f.Date}
	for {
		if !spend() {
			return errBudget
		}
		var c tgclient.Changes
		var err error
		if b.peer == nil {
			c, err = s.Feeds.Changes(ctx, st)
		} else {
			c, err = s.Feeds.ChannelChanges(ctx, b.peer, st.Pts)
		}
		if tgerr.Is(err, "PERSISTENT_TIMESTAMP_INVALID", "PERSISTENT_TIMESTAMP_EMPTY") {
			s.Log.Warn("Telegram no longer knows where the change feed was read to; it goes on from now", "feed", feedName(b.id), "err", err)
			return s.restartFeed(ctx, b, spend)
		}
		if err != nil {
			return err
		}
		if c.TooLong {
			s.Log.Info("Telegram's change feed lost track since the last run; it goes on from now", "feed", feedName(b.id))
		}
		if err := s.apply(ctx, c, byID, spend, r); err != nil {
			return err
		}
		if c.State.Pts == 0 {
			return s.Store.ForgetFeed(ctx, b.id)
		}
		st = c.State
		if err := s.Store.SetFeed(ctx, store.Feed{Box: b.id, Pts: st.Pts, Qts: st.Qts, Date: st.Date, ReadAt: s.Now().Unix()}); err != nil {
			return err
		}
		if c.Final {
			return nil
		}
	}
}

// restartFeed follows a feed from now on: the account's own from where
// Telegram says it stands, a channel's from where its history next says (Run).
func (s *Syncer) restartFeed(ctx context.Context, b feedBox, spend func() bool) error {
	if b.peer != nil {
		return s.Store.ForgetFeed(ctx, b.id)
	}
	if !spend() {
		return errBudget
	}
	st, err := s.Feeds.FeedStart(ctx)
	if err != nil {
		return err
	}
	return s.Store.SetFeed(ctx, store.Feed{Box: 0, Pts: st.Pts, Qts: st.Qts, Date: st.Date, ReadAt: s.Now().Unix()})
}

// chatChanges are a page's changes in one chat.
type chatChanges struct {
	edited           []tg.MessageClass
	touched, deleted []int
}

// apply brings the archived messages a page of a feed names up to date. A
// chat Telegram says is gone, or whose ids it does not take, is skipped;
// anything else stops the feed before it notes the page, so the next run
// reads the page again.
func (s *Syncer) apply(ctx context.Context, c tgclient.Changes, byID map[int64]tgclient.Target, spend func() bool, r *Report) error {
	byChat := map[int64]*chatChanges{}
	at := func(chat int64) *chatChanges {
		if byChat[chat] == nil {
			byChat[chat] = &chatChanges{}
		}
		return byChat[chat]
	}
	for _, m := range c.Edited {
		if chat, ok := chatOf(m); ok {
			at(chat).edited = append(at(chat).edited, m)
		}
	}
	for chat, ids := range c.Touched {
		at(chat).touched = append(at(chat).touched, ids...)
	}
	for chat, ids := range c.Deleted {
		at(chat).deleted = append(at(chat).deleted, ids...)
	}
	if len(c.CommonDeleted) > 0 {
		found, err := s.Store.CommonChats(ctx, c.CommonDeleted)
		if err != nil {
			return err
		}
		for chat, ids := range found {
			at(chat).deleted = append(at(chat).deleted, ids...)
		}
	}
	for _, chat := range slices.Sorted(maps.Keys(byChat)) {
		t, ok := byID[chat]
		if !ok {
			continue
		}
		err := s.applyChat(ctx, t, byChat[chat], c, spend, r)
		switch {
		case err == nil, tgclient.Classify(err) == tgclient.KindInaccessible: // the chat's sync reports it
		case tgerr.Is(err, "MSG_ID_INVALID"):
			s.Log.Warn("change feed: Telegram did not give a chat's changed messages", "chat", chat, "err", err)
		default:
			return err
		}
	}
	return nil
}

// applyChat saves the messages the feed gives whole when they are a later
// edit than the archive's, and reads the others again by id, which also
// confirms a deletion. Messages the archive does not have are the forward
// sync's and the backfill's.
func (s *Syncer) applyChat(ctx context.Context, t tgclient.Target, ch *chatChanges, c tgclient.Changes, spend func() bool, r *Report) error {
	ids := append(append(idsOf(ch.edited), ch.touched...), ch.deleted...)
	known, err := s.Store.MessageStates(ctx, t.Chat.ID, ids)
	if err != nil {
		return err
	}
	var whole []tg.MessageClass
	reread := map[int]bool{}
	for _, m := range ch.edited {
		k, ok := known[m.GetID()]
		switch {
		case !ok:
		case k.Deleted:
			reread[m.GetID()] = true // only Telegram as it is now takes the mark away
		case editDate(m) < k.EditDate:
			// A later version came first (the recent window or the audit).
		case editDate(m) == k.EditDate:
			// The archive's version, or a change that is no edit (reactions):
			// the feed's copy may be older than the archive's, Telegram as it
			// is now is not.
			reread[m.GetID()] = true
		default:
			whole = append(whole, m)
		}
	}
	for _, id := range ch.touched {
		if _, ok := known[id]; ok {
			reread[id] = true
		}
	}
	for _, id := range ch.deleted {
		if k, ok := known[id]; ok && !k.Deleted {
			reread[id] = true
		}
	}
	if len(whole) > 0 {
		cur, _, err := s.Store.GetCursor(ctx, t.Chat.ID)
		if err != nil {
			return err
		}
		st, err := s.save(ctx, t, tgclient.Page{Messages: whole, Users: c.Users, Chats: c.Chats}, cur)
		r.Edited += st.Edited
		r.Updated += st.Updated
		if err != nil {
			return err
		}
	}
	if len(reread) == 0 {
		return nil
	}
	st, err := s.recheck(ctx, t, slices.Sorted(maps.Keys(reread)), spend)
	r.count(st)
	return err
}

// stopsFeeds: the run is cancelled, waits on Telegram or may not make the
// request (420: a flood wait, a frozen account), or lost its session (401);
// the other feeds would fail the same way.
func stopsFeeds(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if rpc, ok := tgerr.As(err); ok && (rpc.Code == 401 || rpc.Code == 420) {
		return true
	}
	k := tgclient.Classify(err)
	return k == tgclient.KindFlood || k == tgclient.KindAuth || k == tgclient.KindCanceled
}

// feedName names a feed in the log: the account's own, or the channel's
// chat id. The account's own feed brings some channels' changes too.
func feedName(box int64) any {
	if box == 0 {
		return "account"
	}
	return box
}

func (s *Syncer) feedProgress(done, total int) {
	if s.FeedProgress != nil {
		s.FeedProgress(done, total)
	}
}

// chatOf is the archive's chat of a message, from its peer.
func chatOf(m tg.MessageClass) (int64, bool) {
	p, ok := m.(interface{ GetPeerID() tg.PeerClass })
	if !ok {
		return 0, false
	}
	return convert.PeerID(p.GetPeerID()), true
}

func editDate(m tg.MessageClass) int64 {
	if msg, ok := m.(*tg.Message); ok {
		d, _ := msg.GetEditDate()
		return int64(d)
	}
	return 0
}

func idsOf(ms []tg.MessageClass) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.GetID()
	}
	return out
}
