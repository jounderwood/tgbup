//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/e2e/testenv"
)

// TestSpikeChangeFeed records, on the test servers, what Telegram's change
// feed and history hash give for edits, reactions and deletions: the facts
// the plan for edits rests on. It only logs; run it by hand:
// TGBUP_E2E_SPIKE=1 TGBUP_TEST_DC=1 go test -tags e2e -run TestSpike -v ./e2e/
func TestSpikeChangeFeed(t *testing.T) {
	if os.Getenv("TGBUP_E2E_SPIKE") == "" {
		t.Skip("by hand: TGBUP_E2E_SPIKE=1")
	}
	err := testenv.WithTelegram(func(ctx context.Context, raw *tg.Client) error {
		ch := fx.Peer()
		channel := &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}
		group := &tg.InputPeerChat{ChatID: fx.GroupID}

		state, err := raw.UpdatesGetState(ctx)
		if err != nil {
			return fmt.Errorf("getState: %w", err)
		}
		t.Logf("state: pts=%d qts=%d date=%d seq=%d", state.Pts, state.Qts, state.Date, state.Seq)
		hist, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ch, Limit: 5})
		if err != nil {
			return err
		}
		chPts := 0
		var page []tg.MessageClass
		if cm, ok := hist.(*tg.MessagesChannelMessages); ok {
			chPts, page = cm.Pts, cm.Messages
		}
		t.Logf("channel pts from getHistory: %d", chPts)

		// Changes, each on its own message: edited and reacted to, or deleted.
		send := func(peer tg.InputPeerClass, text string) int {
			id, err := testenv.SendText(ctx, raw, peer, text, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		chEdit, chDel := send(ch, "spike: to edit"), send(ch, "spike: to delete")
		grEdit, grDel := send(group, "spike group: to edit"), send(group, "spike group: to delete")
		t.Logf("channel: edit %d, delete %d; group: edit %d, delete %d", chEdit, chDel, grEdit, grDel)
		// The new messages move the feeds on; the state after them is the
		// start, so the feeds below hold only the edits and deletions.
		state, err = raw.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		if hist, err = raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ch, Limit: 5}); err != nil {
			return err
		}
		if cm, ok := hist.(*tg.MessagesChannelMessages); ok {
			chPts, page = cm.Pts, cm.Messages
		}
		t.Logf("after sending: pts=%d channel pts=%d", state.Pts, chPts)
		for _, x := range []struct {
			peer tg.InputPeerClass
			id   int
		}{{ch, chEdit}, {group, grEdit}} {
			if _, err := raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: x.peer, ID: x.id, Message: "spike: edited"}); err != nil {
				return fmt.Errorf("edit: %w", err)
			}
			if _, err := raw.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: x.peer, MsgID: x.id,
				Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}}}); err != nil {
				t.Logf("reaction on %T: %v", x.peer, err)
			}
		}
		if _, err := raw.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: channel, ID: []int{chDel}}); err != nil {
			return fmt.Errorf("delete in channel: %w", err)
		}
		if _, err := raw.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{grDel}}); err != nil {
			return fmt.Errorf("delete in group: %w", err)
		}

		// The common feed: private chats and basic groups.
		diff, err := raw.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: state.Pts, Date: state.Date, Qts: state.Qts})
		if err != nil {
			return fmt.Errorf("getDifference: %w", err)
		}
		switch d := diff.(type) {
		case *tg.UpdatesDifference:
			t.Logf("getDifference: %d new messages", len(d.NewMessages))
			for _, u := range d.OtherUpdates {
				t.Logf("  other: %T %+v", u, u)
			}
		case *tg.UpdatesDifferenceSlice:
			t.Logf("getDifference slice: %d new messages, %d other", len(d.NewMessages), len(d.OtherUpdates))
			for _, u := range d.OtherUpdates {
				t.Logf("  other: %T", u)
			}
		default:
			t.Logf("getDifference: %T", diff)
		}

		// The channel's own feed.
		cd, err := raw.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{
			Channel: channel, Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: chPts, Limit: 100})
		if err != nil {
			return fmt.Errorf("getChannelDifference: %w", err)
		}
		switch d := cd.(type) {
		case *tg.UpdatesChannelDifference:
			t.Logf("getChannelDifference: final=%v pts=%d, %d new messages", d.Final, d.Pts, len(d.NewMessages))
			for _, u := range d.OtherUpdates {
				t.Logf("  other: %T %+v", u, u)
			}
		default:
			t.Logf("getChannelDifference: %T %+v", cd, cd)
		}

		// The history hash: does Telegram answer "not modified" to a page it
		// already gave, and does an edit or a reaction change that?
		hashes := map[string]int64{"ids": historyHash(page, false), "ids+edit": historyHash(page, true)}
		for name, h := range hashes {
			res, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ch, Limit: 5, Hash: h})
			if err != nil {
				t.Logf("hash %s: %v", name, err)
				continue
			}
			_, notModified := res.(*tg.MessagesMessagesNotModified)
			t.Logf("hash %s = %d: not modified %v (%T)", name, h, notModified, res)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// historyHash is Telegram's documented hash over a list of ids (and, as a
// guess, edit dates).
func historyHash(ms []tg.MessageClass, withEdit bool) int64 {
	var h uint64
	add := func(v uint64) {
		h ^= h >> 21
		h ^= h << 35
		h ^= h >> 4
		h += v
	}
	for _, m := range ms {
		add(uint64(m.GetID()))
		if withEdit {
			if msg, ok := m.(*tg.Message); ok {
				d, _ := msg.GetEditDate()
				add(uint64(d))
			}
		}
	}
	return int64(h)
}
