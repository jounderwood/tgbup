package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type Syncer struct {
	TG       tgclient.Telegram
	Store    *store.Store
	SelfID   int64
	Budget   time.Duration
	PageSize int
	Now      func() time.Time
	Log      *slog.Logger
}

type Report struct {
	Chats             int
	NewMessages       int
	Deferred          []int64
	NewlyInaccessible []int64
	Failed            map[int64]error
	AuthErr           error
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
		n, err := s.syncChat(ctx, t, deadline)
		r.NewMessages += n
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
	return r
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

func (s *Syncer) syncChat(ctx context.Context, t tgclient.Target, deadline time.Time) (int, error) {
	if err := s.Store.UpsertChat(ctx, t.Chat); err != nil {
		return 0, err
	}
	if t.IsForum() {
		if err := s.syncTopics(ctx, t); err != nil {
			return 0, err
		}
		reset, err := s.Store.ApplyTopicExclusions(ctx, t.Chat.ID, t.ExcludedTopics)
		if err != nil {
			return 0, err
		}
		if reset {
			s.Log.Info("topic re-included, reading the forum history again", "chat", t.Chat.ID)
		}
	}
	cur, found, err := s.Store.GetCursor(ctx, t.Chat.ID)
	if err != nil {
		return 0, err
	}
	total := 0

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
			n, err := s.save(ctx, t, page, cur)
			total += n
			if err != nil {
				return total, err
			}
			if len(page.Messages) < s.PageSize {
				break
			}
		}
		// A server that ignores the forward-paging window would otherwise make
		// every run silently archive nothing new.
		if t.TopMessage > cur.NewestID {
			return total, fmt.Errorf("forward sync stopped at message %d, but the chat's latest message is %d", cur.NewestID, t.TopMessage)
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
		n, err := s.save(ctx, t, page, cur)
		total += n
		if err != nil {
			return total, err
		}
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

func (s *Syncer) syncTopics(ctx context.Context, t tgclient.Target) error {
	topics, err := s.TG.ForumTopics(ctx, t.Peer)
	if err != nil {
		return err
	}
	rows := make([]store.Topic, 0, len(topics))
	known := map[int]bool{}
	for _, tp := range topics {
		rows = append(rows, store.Topic{ChatID: t.Chat.ID, TopicID: tp.ID, Title: tp.Title, Closed: tp.Closed, CreatedAt: int64(tp.Date)})
		known[tp.ID] = true
	}
	for _, id := range t.ExcludedTopics {
		if !known[id] {
			s.Log.Warn("exclude_topics: no such topic in the forum", "chat", t.Chat.ID, "topic", id)
		}
	}
	return s.Store.UpsertTopics(ctx, rows)
}

func (s *Syncer) save(ctx context.Context, t tgclient.Target, page tgclient.Page, cur store.Cursor) (int, error) {
	b := store.Batch{ChatID: t.Chat.ID, Users: convert.Users(page.Users), Peers: convert.Peers(page.Chats), Cursor: cur}
	seen := map[string]bool{}
	opts := convert.Options{IsForum: t.IsForum(), SelfID: s.SelfID}
	for _, mc := range page.Messages {
		res, ok, err := convert.Message(mc, opts)
		if err != nil {
			return 0, err
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
	return len(b.Messages), s.Store.SaveBatch(ctx, b)
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
