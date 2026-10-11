package tgclient

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/convert"
)

// Feeds reads Telegram's change feeds: the account's own (private chats and
// basic groups) and a channel's or supergroup's.
type Feeds interface {
	FeedStart(ctx context.Context) (FeedState, error)
	Changes(ctx context.Context, st FeedState) (Changes, error)
	ChannelChanges(ctx context.Context, peer tg.InputPeerClass, pts int) (Changes, error)
}

// FeedState is where one of Telegram's change feeds stands: the account's
// own (private chats and basic groups: pts, qts and date) or a channel's or
// supergroup's (pts alone).
type FeedState struct{ Pts, Qts, Date int }

// Changes is a page of a change feed: what happened to messages after a
// state. New messages are left out: the forward sync reads those.
type Changes struct {
	// Edited holds messages as they are now, edited or with other
	// reactions; each one's peer says its chat.
	Edited []tg.MessageClass
	// Touched lists, by chat id, messages that changed but did not come
	// whole (reactions): to be read again.
	Touched map[int64][]int
	// Deleted lists, by chat id, the ids deleted in channels and
	// supergroups. CommonDeleted are those deleted in private chats and
	// basic groups, which do not say their chat.
	Deleted       map[int64][]int
	CommonDeleted []int
	Users         []tg.UserClass
	Chats         []tg.ChatClass
	// State is where the next page starts; Final: there is nothing more for
	// now.
	State FeedState
	Final bool
	// TooLong: Telegram no longer has every change after the state asked
	// about. State is where the feed stands now (Pts 0: unknown).
	TooLong bool
}

// channelFeedLimit is the largest limit. Telegram gives an account 100
// updates a page whatever the limit, but answers "too long" when more than
// the limit piled up since the state asked about, instead of paging.
const channelFeedLimit = 100000

// FeedStart says where the account's own change feed stands now.
func (a *API) FeedStart(ctx context.Context) (FeedState, error) {
	s, err := a.raw.UpdatesGetState(ctx)
	if err != nil {
		return FeedState{}, err
	}
	return FeedState{Pts: s.Pts, Qts: s.Qts, Date: s.Date}, nil
}

// Changes reads the account's own change feed after st, a page at a time.
func (a *API) Changes(ctx context.Context, st FeedState) (Changes, error) {
	res, err := a.raw.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: st.Pts, Qts: st.Qts, Date: st.Date})
	if err != nil {
		return Changes{}, err
	}
	return differenceChanges(st, res)
}

// ChannelChanges reads a channel's or supergroup's change feed after pts, a
// page at a time.
func (a *API) ChannelChanges(ctx context.Context, peer tg.InputPeerClass, pts int) (Changes, error) {
	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return Changes{}, fmt.Errorf("%T has no change feed of its own", peer)
	}
	res, err := a.raw.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{}, Pts: pts, Limit: channelFeedLimit,
	})
	if err != nil {
		return Changes{}, err
	}
	return channelChanges(res)
}

func differenceChanges(st FeedState, res tg.UpdatesDifferenceClass) (Changes, error) {
	switch d := res.(type) {
	case *tg.UpdatesDifferenceEmpty:
		st.Date = d.Date
		return Changes{State: st, Final: true}, nil
	case *tg.UpdatesDifference:
		c := updateChanges(d.OtherUpdates)
		c.Users, c.Chats = d.Users, d.Chats
		c.State, c.Final = FeedState{Pts: d.State.Pts, Qts: d.State.Qts, Date: d.State.Date}, true
		return c, nil
	case *tg.UpdatesDifferenceSlice:
		c := updateChanges(d.OtherUpdates)
		c.Users, c.Chats = d.Users, d.Chats
		s := d.IntermediateState
		c.State = FeedState{Pts: s.Pts, Qts: s.Qts, Date: s.Date}
		return c, nil
	case *tg.UpdatesDifferenceTooLong:
		// The rest comes from the new pts, as Telegram's own apps go on.
		st.Pts = d.Pts
		return Changes{State: st, TooLong: true}, nil
	}
	return Changes{}, fmt.Errorf("unexpected updates.getDifference result %T", res)
}

func channelChanges(res tg.UpdatesChannelDifferenceClass) (Changes, error) {
	switch d := res.(type) {
	case *tg.UpdatesChannelDifferenceEmpty:
		return Changes{State: FeedState{Pts: d.Pts}, Final: true}, nil
	case *tg.UpdatesChannelDifference:
		c := updateChanges(d.OtherUpdates)
		c.Users, c.Chats = d.Users, d.Chats
		c.State, c.Final = FeedState{Pts: d.Pts}, d.Final
		return c, nil
	case *tg.UpdatesChannelDifferenceTooLong:
		c := Changes{Final: true, TooLong: true}
		if dl, ok := d.Dialog.(*tg.Dialog); ok {
			c.State.Pts, _ = dl.GetPts()
		}
		return c, nil
	}
	return Changes{}, fmt.Errorf("unexpected updates.getChannelDifference result %T", res)
}

// updateChanges sorts a feed's updates. Others (read marks, views, pins,
// votes, new messages) are left to the rest of the run.
func updateChanges(us []tg.UpdateClass) Changes {
	c := Changes{Touched: map[int64][]int{}, Deleted: map[int64][]int{}}
	for _, u := range us {
		switch x := u.(type) {
		case *tg.UpdateEditMessage:
			c.Edited = append(c.Edited, x.Message)
		case *tg.UpdateEditChannelMessage:
			c.Edited = append(c.Edited, x.Message)
		case *tg.UpdateMessageReactions:
			chat := convert.PeerID(x.Peer)
			c.Touched[chat] = append(c.Touched[chat], x.MsgID)
		case *tg.UpdateDeleteChannelMessages:
			chat := convert.ChannelID(x.ChannelID)
			c.Deleted[chat] = append(c.Deleted[chat], x.Messages...)
		case *tg.UpdateDeleteMessages:
			c.CommonDeleted = append(c.CommonDeleted, x.Messages...)
		}
	}
	return c
}
