// Package stats estimates what an archive run would fetch: message totals,
// counts per media type and per forum topic, without walking any history.
package stats

import (
	"context"
	"slices"

	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

type Counter interface {
	HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error)
	SearchCounters(ctx context.Context, peer tg.InputPeerClass) (tgclient.TypeCounts, error)
	ForumTopics(ctx context.Context, peer tg.InputPeerClass) ([]tgclient.Topic, error)
	TopicMessageCount(ctx context.Context, peer tg.InputPeerClass, topicID int) (int, error)
}

type Item struct {
	ID             int64
	Type           string
	Title          string
	Peer           tg.InputPeerClass
	ExcludedTopics []int
	// UpgradedTo is the supergroup this basic group was upgraded to, if any:
	// such a group holds the history from before the upgrade.
	UpgradedTo int64
}

type TopicStats struct {
	ID       int
	Title    string
	Count    int
	Excluded bool
}

type ChatStats struct {
	Item   Item
	Total  int
	Types  tgclient.TypeCounts
	Topics []TopicStats
	// MissingTopics are excluded topic ids the forum no longer has.
	MissingTopics []int
	Err           error
}

// Saved is the message count that will actually be archived.
func (c ChatStats) Saved() int {
	n := c.Total
	for _, t := range c.Topics {
		if t.Excluded {
			n -= t.Count
		}
	}
	return max(n, 0)
}

// Collect gathers stats chat by chat; a failure in one chat is recorded in
// its Err and does not stop the others.
func Collect(ctx context.Context, c Counter, items []Item, progress func(done, total int)) []ChatStats {
	out := make([]ChatStats, 0, len(items))
	for i, it := range items {
		cs := ChatStats{Item: it}
		if err := ctx.Err(); err != nil {
			cs.Err = err
		} else {
			cs.Err = collectChat(ctx, c, &cs)
		}
		out = append(out, cs)
		if progress != nil {
			progress(i+1, len(items))
		}
	}
	return out
}

func collectChat(ctx context.Context, c Counter, cs *ChatStats) error {
	var err error
	if cs.Total, err = c.HistoryCount(ctx, cs.Item.Peer); err != nil {
		return err
	}
	if cs.Types, err = c.SearchCounters(ctx, cs.Item.Peer); err != nil {
		return err
	}
	if cs.Item.Type != store.ChatForum {
		return nil
	}
	topics, err := c.ForumTopics(ctx, cs.Item.Peer)
	if err != nil {
		return err
	}
	general, rest := -1, 0
	for _, t := range topics {
		ts := TopicStats{ID: t.ID, Title: t.Title, Excluded: slices.Contains(cs.Item.ExcludedTopics, t.ID)}
		// General is not a reply thread, so its count is what the other topics leave.
		if t.ID == 1 {
			general = len(cs.Topics)
		} else if ts.Count, err = c.TopicMessageCount(ctx, cs.Item.Peer, t.ID); err != nil {
			return err
		}
		rest += ts.Count
		cs.Topics = append(cs.Topics, ts)
	}
	if general >= 0 {
		cs.Topics[general].Count = max(cs.Total-rest, 0)
	}
	for _, id := range cs.Item.ExcludedTopics {
		if !slices.ContainsFunc(topics, func(t tgclient.Topic) bool { return t.ID == id }) {
			cs.MissingTopics = append(cs.MissingTopics, id)
		}
	}
	return nil
}
