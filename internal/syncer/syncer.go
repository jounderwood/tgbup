package syncer

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

type Syncer struct {
	TG       tgclient.Telegram
	Store    *store.Store
	SelfID   int64
	Budget   time.Duration
	PageSize int
	Now      func() time.Time
	Log      *slog.Logger
	// Progress, if set, gets the messages saved so far in a chat after each
	// page, and once more with done=true when the chat is finished.
	Progress func(chat store.Chat, saved int, done bool)
	// RefreshWindow is how far back every run reads again the messages the
	// archive has, for edits, reactions and votes, which mostly come soon
	// after a message; RefreshPages caps it per chat. 0: the defaults.
	RefreshWindow time.Duration
	RefreshPages  int
	// AuditPages caps the pages the audit reads again in a run, over all
	// chats: older edits, reactions and deletions, a round at a time. 0: the
	// default; negative: off.
	AuditPages int
}

const (
	defaultRefreshWindow = 72 * time.Hour
	defaultRefreshPages  = 3
	defaultAuditPages    = 100
)

type Report struct {
	Chats       int
	NewMessages int
	// Edited and Updated count messages read again that had changed: edited
	// (the version before is kept), or with other reactions, votes, a link
	// preview. Deleted ones are gone from Telegram, marked and kept.
	Edited, Updated, Deleted int
	Deferred                 []int64
	NewlyInaccessible        []int64
	Failed                   map[int64]error
	AuthErr                  error
}

func (r Report) OK() bool { return len(r.Failed) == 0 && r.AuthErr == nil }

func (s *Syncer) Run(ctx context.Context, targets []tgclient.Target, dialogIDs map[int64]bool) Report {
	r := Report{Failed: map[int64]error{}}
	deadline := s.Now().Add(s.Budget)

	known := map[int64]bool{}
	for id := range dialogIDs {
		known[id] = true
	}
	for _, t := range targets {
		known[t.Chat.ID] = true
	}
	if err := s.markMissing(ctx, known, &r); err != nil {
		r.Failed[0] = fmt.Errorf("mark missing chats: %w", err)
	}

	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			r.Failed[t.Chat.ID] = err
			return r
		}
		st, err := s.syncChat(ctx, t, deadline)
		r.NewMessages += st.New
		r.Edited += st.Edited
		r.Updated += st.Updated
		r.Deleted += st.Deleted
		s.progress(t.Chat, st.New, true)
		if err == nil {
			r.Chats++
			continue
		}
		switch tgclient.Classify(err) {
		case tgclient.KindFlood:
			s.Log.Warn("chat deferred by flood wait", "chat", t.Chat.ID, "err", err)
			r.Deferred = append(r.Deferred, t.Chat.ID)
		case tgclient.KindInaccessible:
			changed, merr := s.Store.MarkInaccessible(ctx, t.Chat.ID)
			switch {
			case merr != nil:
				r.Failed[t.Chat.ID] = merr
			case changed:
				r.NewlyInaccessible = append(r.NewlyInaccessible, t.Chat.ID)
			}
		case tgclient.KindAuth:
			r.AuthErr = err
			return r
		default:
			s.Log.Error("chat sync failed", "chat", t.Chat.ID, "err", err)
			r.Failed[t.Chat.ID] = err
		}
	}
	s.audit(ctx, targets, deadline, &r)
	return r
}

// audit reads the archived chats' history again, newest to oldest, a round
// at a time and at most AuditPages pages a run, with what time is left:
// edits, reactions and deletions older than the recent window reach the
// archive within a few runs. The least recently audited chats go first. It
// never fails the run: what it does not reach, the next run does.
func (s *Syncer) audit(ctx context.Context, targets []tgclient.Target, deadline time.Time, r *Report) {
	budget := cmp.Or(s.AuditPages, defaultAuditPages)
	if budget < 0 {
		return
	}
	byID := map[int64]tgclient.Target{}
	for _, t := range targets {
		if r.Failed[t.Chat.ID] == nil && !slices.Contains(r.Deferred, t.Chat.ID) {
			byID[t.Chat.ID] = t
		}
	}
	audits, err := s.Store.Audits(ctx)
	if err != nil {
		s.Log.Warn("audit", "err", err)
		return
	}
	for _, a := range audits {
		t, ok := byID[a.ChatID]
		if !ok {
			continue
		}
		cur, _, err := s.Store.GetCursor(ctx, t.Chat.ID)
		if err != nil {
			s.Log.Warn("audit", "chat", t.Chat.ID, "err", err)
			return
		}
		for {
			if budget == 0 || !s.Now().Before(deadline) || ctx.Err() != nil {
				return
			}
			budget--
			page, err := s.TG.HistoryBefore(ctx, t.Peer, a.BeforeID, s.PageSize)
			if err != nil {
				s.Log.Warn("audit stopped; the next run goes on", "chat", t.Chat.ID, "err", err)
				return
			}
			st, err := s.save(ctx, t, page, cur)
			r.NewMessages += st.New
			r.Edited += st.Edited
			r.Updated += st.Updated
			if err != nil {
				s.Log.Warn("audit", "chat", t.Chat.ID, "err", err)
				return
			}
			if len(page.Messages) > 0 {
				hi := a.BeforeID - 1
				if a.BeforeID == 0 {
					hi = math.MaxInt32
				}
				n, err := s.markDeleted(ctx, t, page, minID(page.Messages), hi)
				r.Deleted += n
				if err != nil {
					s.Log.Warn("audit", "chat", t.Chat.ID, "err", err)
					return
				}
			}
			done := len(page.Messages) < s.PageSize
			if done {
				a.BeforeID, a.AuditedAt = 0, s.Now().Unix()
			} else {
				a.BeforeID = minID(page.Messages)
			}
			if err := s.Store.SetAudit(ctx, a); err != nil {
				s.Log.Warn("audit", "chat", t.Chat.ID, "err", err)
				return
			}
			if done {
				break
			}
		}
	}
}

func (s *Syncer) markMissing(ctx context.Context, known map[int64]bool, r *Report) error {
	ids, err := s.Store.ActiveChatIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if known[id] {
			continue
		}
		changed, err := s.Store.MarkInaccessible(ctx, id)
		if err != nil {
			return err
		}
		if changed {
			r.NewlyInaccessible = append(r.NewlyInaccessible, id)
		}
	}
	return nil
}

func (s *Syncer) syncChat(ctx context.Context, t tgclient.Target, deadline time.Time) (syncStats, error) {
	var total syncStats
	if err := s.Store.UpsertChat(ctx, t.Chat); err != nil {
		return total, err
	}
	if t.IsForum() {
		topics, err := s.syncTopics(ctx, t)
		if err != nil {
			return total, err
		}
		reset, err := s.Store.ApplyTopicExclusions(ctx, t.Chat.ID, t.ExcludedTopics, topics)
		if err != nil {
			return total, err
		}
		if reset {
			s.Log.Info("topic re-included, reading the forum history again", "chat", t.Chat.ID)
		}
	}
	cur, found, err := s.Store.GetCursor(ctx, t.Chat.ID)
	if err != nil {
		return total, err
	}

	if found && (cur.NewestID > 0 || cur.BackfillDone) {
		for {
			page, err := s.TG.HistoryAfter(ctx, t.Peer, cur.NewestID, s.PageSize)
			if err != nil {
				return total, err
			}
			if len(page.Messages) == 0 {
				break
			}
			cur.NewestID = max(cur.NewestID, maxID(page.Messages))
			st, err := s.save(ctx, t, page, cur)
			total.SaveStats = add(total.SaveStats, st)
			if err != nil {
				return total, err
			}
			s.progress(t.Chat, total.New, false)
			if len(page.Messages) < s.PageSize {
				break
			}
		}
		// A server that ignores the forward-paging window would otherwise make
		// every run silently archive nothing new.
		if t.TopMessage > cur.NewestID {
			return total, fmt.Errorf("forward sync stopped at message %d, but the chat's latest message is %d", cur.NewestID, t.TopMessage)
		}
		st, err := s.refresh(ctx, t, &cur)
		total.SaveStats = add(total.SaveStats, st.SaveStats)
		total.Deleted += st.Deleted
		if err != nil {
			return total, err
		}
	}

	for !cur.BackfillDone && s.Now().Before(deadline) {
		page, err := s.TG.HistoryBefore(ctx, t.Peer, cur.OldestID, s.PageSize)
		if err != nil {
			return total, err
		}
		if len(page.Messages) == 0 {
			cur.BackfillDone = true
			if err := s.Store.SaveBatch(ctx, store.Batch{ChatID: t.Chat.ID, Cursor: cur}); err != nil {
				return total, err
			}
			break
		}
		if cur.NewestID == 0 {
			cur.NewestID = maxID(page.Messages)
		}
		cur.OldestID = minID(page.Messages)
		st, err := s.save(ctx, t, page, cur)
		total.SaveStats = add(total.SaveStats, st)
		if err != nil {
			return total, err
		}
		s.progress(t.Chat, total.New, false)
	}

	pinned, err := s.TG.PinnedIDs(ctx, t.Peer)
	if err != nil {
		return total, err
	}
	if err := s.Store.SetPinned(ctx, t.Chat.ID, pinned); err != nil {
		return total, err
	}
	return total, s.Store.MarkActive(ctx, t.Chat.ID)
}

// syncStats is what a chat's sync changed.
type syncStats struct {
	store.SaveStats
	Deleted int
}

// refresh reads again the chat's latest messages, back to RefreshWindow and
// at most RefreshPages pages, so that edits, reactions and votes reach the
// archive, and messages deleted in Telegram are marked. A chat with nothing
// that recent is not asked.
func (s *Syncer) refresh(ctx context.Context, t tgclient.Target, cur *store.Cursor) (syncStats, error) {
	var total syncStats
	since := s.Now().Add(-cmp.Or(s.RefreshWindow, defaultRefreshWindow)).Unix()
	latest, ok, err := s.Store.LatestMessageDate(ctx, t.Chat.ID)
	if err != nil || !ok || latest < since {
		return total, err
	}
	before := 0
	for range cmp.Or(s.RefreshPages, defaultRefreshPages) {
		page, err := s.TG.HistoryBefore(ctx, t.Peer, before, s.PageSize)
		if err != nil || len(page.Messages) == 0 {
			return total, err
		}
		cur.NewestID = max(cur.NewestID, maxID(page.Messages))
		st, err := s.save(ctx, t, page, *cur)
		total.SaveStats = add(total.SaveStats, st)
		if err != nil {
			return total, err
		}
		// The page is every message Telegram has from its oldest id up to
		// before (the first page: up to the newest): an archived one missing
		// from it may be deleted.
		hi := before - 1
		if before == 0 {
			hi = math.MaxInt32
		}
		n, err := s.markDeleted(ctx, t, page, minID(page.Messages), hi)
		total.Deleted += n
		if err != nil || len(page.Messages) < s.PageSize || oldestDate(page.Messages) < since {
			return total, err
		}
		before = minID(page.Messages)
	}
	return total, nil
}

// markDeleted marks the archived messages with ids from lo to hi that the
// page lacks, once Telegram confirms each is gone: asked by id, it gives
// back nothing or an empty message. One it still gives is left alone.
func (s *Syncer) markDeleted(ctx context.Context, t tgclient.Target, page tgclient.Page, lo, hi int) (int, error) {
	archived, err := s.Store.ArchivedIDs(ctx, t.Chat.ID, lo, hi)
	if err != nil || len(archived) == 0 {
		return 0, err
	}
	got := map[int]bool{}
	for _, m := range page.Messages {
		if _, empty := m.(*tg.MessageEmpty); !empty {
			got[m.GetID()] = true
		}
	}
	var missing []int
	for _, id := range archived {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	marked := 0
	for len(missing) > 0 {
		batch := missing[:min(len(missing), 100)]
		missing = missing[len(batch):]
		res, err := s.TG.Messages(ctx, t.Peer, batch)
		if err != nil {
			return marked, err
		}
		there := map[int]bool{}
		for _, m := range res.Messages {
			if _, empty := m.(*tg.MessageEmpty); !empty {
				there[m.GetID()] = true
			}
		}
		var gone []int
		for _, id := range batch {
			if !there[id] {
				gone = append(gone, id)
			}
		}
		n, err := s.Store.MarkDeleted(ctx, t.Chat.ID, gone, s.Now().Unix())
		marked += n
		if err != nil {
			return marked, err
		}
	}
	return marked, nil
}

func add(a, b store.SaveStats) store.SaveStats {
	return store.SaveStats{New: a.New + b.New, Edited: a.Edited + b.Edited, Updated: a.Updated + b.Updated}
}

func oldestDate(ms []tg.MessageClass) int64 {
	oldest := int64(0)
	for _, m := range ms {
		var d int
		switch v := m.(type) {
		case *tg.Message:
			d = v.Date
		case *tg.MessageService:
			d = v.Date
		default:
			continue
		}
		if oldest == 0 || int64(d) < oldest {
			oldest = int64(d)
		}
	}
	return oldest
}

// syncTopics stores the forum's topics and returns their ids.
func (s *Syncer) progress(c store.Chat, saved int, done bool) {
	if s.Progress != nil {
		s.Progress(c, saved, done)
	}
}

func (s *Syncer) syncTopics(ctx context.Context, t tgclient.Target) ([]int, error) {
	topics, err := s.TG.ForumTopics(ctx, t.Peer)
	if err != nil {
		return nil, err
	}
	rows := make([]store.Topic, 0, len(topics))
	ids := make([]int, 0, len(topics))
	for _, tp := range topics {
		rows = append(rows, store.Topic{ChatID: t.Chat.ID, TopicID: tp.ID, Title: tp.Title, Closed: tp.Closed, CreatedAt: int64(tp.Date)})
		ids = append(ids, tp.ID)
	}
	for _, id := range t.ExcludedTopics {
		if !slices.Contains(ids, id) {
			s.Log.Warn("exclude_topics: no such topic in the forum", "chat", t.Chat.ID, "topic", id)
		}
	}
	return ids, s.Store.UpsertTopics(ctx, rows)
}

func (s *Syncer) save(ctx context.Context, t tgclient.Target, page tgclient.Page, cur store.Cursor) (store.SaveStats, error) {
	b := store.Batch{ChatID: t.Chat.ID, Users: convert.Users(page.Users), Peers: convert.Peers(page.Chats), Cursor: cur}
	seen := map[string]bool{}
	opts := convert.Options{IsForum: t.IsForum(), SelfID: s.SelfID}
	for _, mc := range page.Messages {
		res, ok, err := convert.Message(mc, opts)
		if err != nil {
			return store.SaveStats{}, err
		}
		if !ok || slices.Contains(t.ExcludedTopics, res.Message.TopicID) {
			continue
		}
		b.Messages = append(b.Messages, res.Message)
		for _, m := range res.Media {
			if !seen[m.Key] {
				seen[m.Key] = true
				b.Media = append(b.Media, m)
			}
		}
	}
	return s.Store.SaveBatchStats(ctx, b)
}

func maxID(ms []tg.MessageClass) int {
	out := 0
	for _, m := range ms {
		out = max(out, m.GetID())
	}
	return out
}

func minID(ms []tg.MessageClass) int {
	out := ms[0].GetID()
	for _, m := range ms {
		out = min(out, m.GetID())
	}
	return out
}
