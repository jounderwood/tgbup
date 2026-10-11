//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"

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

// TestSpikeChannelGap records how a channel's change feed pages when more
// updates than the limit piled up since the last run (many new messages, an
// old one edited and one deleted), with and without the filter that leaves
// out new messages, and what it gives for a reaction alone. By hand:
// TGBUP_E2E_SPIKE=1 TGBUP_TEST_DC=1 go test -tags e2e -run TestSpikeChannelGap -v ./e2e/
func TestSpikeChannelGap(t *testing.T) {
	if os.Getenv("TGBUP_E2E_SPIKE") == "" {
		t.Skip("by hand: TGBUP_E2E_SPIKE=1")
	}
	err := testenv.WithTelegram(func(ctx context.Context, raw *tg.Client) error {
		ch := fx.Peer()
		channel := &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}
		pts := func() int {
			hist, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ch, Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			cm, ok := hist.(*tg.MessagesChannelMessages)
			if !ok {
				t.Fatalf("history: %T", hist)
			}
			return cm.Pts
		}
		send := func(text string) int {
			id, err := testenv.SendText(ctx, raw, ch, text, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		describe := func(name string, cd tg.UpdatesChannelDifferenceClass) {
			switch d := cd.(type) {
			case *tg.UpdatesChannelDifferenceEmpty:
				t.Logf("%s: empty final=%v pts=%d", name, d.Final, d.Pts)
			case *tg.UpdatesChannelDifference:
				kinds := map[string]int{}
				for _, u := range d.OtherUpdates {
					kinds[fmt.Sprintf("%T", u)]++
				}
				t.Logf("%s: difference final=%v pts=%d new=%d other=%v", name, d.Final, d.Pts, len(d.NewMessages), kinds)
			case *tg.UpdatesChannelDifferenceTooLong:
				dp := 0
				if dl, ok := d.Dialog.(*tg.Dialog); ok {
					dp, _ = dl.GetPts()
				}
				t.Logf("%s: TOO LONG final=%v dialog pts=%d messages=%d", name, d.Final, dp, len(d.Messages))
			}
		}
		diff := func(name string, from, limit int, filter tg.ChannelMessagesFilterClass) tg.UpdatesChannelDifferenceClass {
			if filter == nil {
				filter = &tg.ChannelMessagesFilterEmpty{}
			}
			cd, err := raw.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{
				Channel: channel, Filter: filter, Pts: from, Limit: limit})
			if err != nil {
				t.Logf("%s: %v", name, err)
				return nil
			}
			describe(name, cd)
			return cd
		}

		// A reaction alone.
		react := send("gap: to react")
		edit, del := send("gap: to edit"), send("gap: to delete")
		p1 := pts()
		if _, err := raw.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: ch, MsgID: react,
			Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}}}); err != nil {
			t.Logf("reaction: %v", err)
		}
		diff("reaction only", p1, 100, nil)

		// A gap of a few hundred updates: forwards of the chat's own messages.
		p2 := pts()
		hist, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ch, Limit: 100})
		if err != nil {
			return err
		}
		var ids []int
		if cm, ok := hist.(*tg.MessagesChannelMessages); ok {
			for _, m := range cm.Messages {
				if msg, ok := m.(*tg.Message); ok && msg.ID != del && msg.ID != edit {
					ids = append(ids, msg.ID)
				}
			}
		}
		for sent := 0; sent < 250; {
			n := min(len(ids), 100)
			rnd := make([]int64, n)
			for i := range rnd {
				var b [8]byte
				_, _ = rand.Read(b[:])
				rnd[i] = int64(binary.LittleEndian.Uint64(b[:]))
			}
			if _, err := raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
				FromPeer: ch, ID: ids[:n], RandomID: rnd, ToPeer: ch}); err != nil {
				return fmt.Errorf("forward: %w", err)
			}
			sent += n
			time.Sleep(time.Second)
		}
		if _, err := raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: ch, ID: edit, Message: "gap: edited"}); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		if _, err := raw.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: channel, ID: []int{del}}); err != nil {
			return fmt.Errorf("delete: %w", err)
		}
		p3 := pts()
		t.Logf("gap: pts %d -> %d (%d updates); edited %d, deleted %d", p2, p3, p3-p2, edit, del)

		for _, limit := range []int{100, 1000, 100000} {
			diff(fmt.Sprintf("limit %d", limit), p2, limit, nil)
		}
		old := &tg.ChannelMessagesFilter{ExcludeNewMessages: true, Ranges: []tg.MessageRange{{MinID: 1, MaxID: del}}}
		noNew := &tg.ChannelMessagesFilter{ExcludeNewMessages: true}
		diff("limit 100, old messages only", p2, 100, old)

		// Paging to the end with the largest limit: how many calls, and where
		// the edit and the deletion show up.
		for _, f := range []struct {
			name   string
			filter tg.ChannelMessagesFilterClass
		}{{"all", nil}, {"no new messages", noNew}, {"old messages only", old}} {
			from, calls := p2, 0
			for calls < 20 {
				calls++
				cd := diff(fmt.Sprintf("paging %s #%d", f.name, calls), from, 100000, f.filter)
				d, ok := cd.(*tg.UpdatesChannelDifference)
				if !ok {
					break
				}
				for _, u := range d.OtherUpdates {
					switch x := u.(type) {
					case *tg.UpdateEditChannelMessage:
						t.Logf("  edit of %d", x.Message.GetID())
					case *tg.UpdateDeleteChannelMessages:
						t.Logf("  deleted %v", x.Messages)
					}
				}
				if d.Final {
					break
				}
				from = d.Pts
			}
			t.Logf("paging %s: %d calls", f.name, calls)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSpikeOwnFeedGap records how the account's own feed pages a gap of a
// few hundred updates (forwards into the basic group, an old message edited
// and one deleted), and whether pts_total_limit or pts_limit change that.
// By hand:
// TGBUP_E2E_SPIKE=1 TGBUP_TEST_DC=1 go test -tags e2e -run TestSpikeOwnFeedGap -v ./e2e/
func TestSpikeOwnFeedGap(t *testing.T) {
	if os.Getenv("TGBUP_E2E_SPIKE") == "" {
		t.Skip("by hand: TGBUP_E2E_SPIKE=1")
	}
	err := testenv.WithTelegram(func(ctx context.Context, raw *tg.Client) error {
		ch, group := fx.Peer(), &tg.InputPeerChat{ChatID: fx.GroupID}
		edit, err := testenv.SendText(ctx, raw, group, "own gap: to edit", nil, 0)
		if err != nil {
			return err
		}
		del, err := testenv.SendText(ctx, raw, group, "own gap: to delete", nil, 0)
		if err != nil {
			return err
		}
		s0, err := raw.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		hist, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ch, Limit: 100})
		if err != nil {
			return err
		}
		var ids []int
		if cm, ok := hist.(*tg.MessagesChannelMessages); ok {
			for _, m := range cm.Messages {
				if _, ok := m.(*tg.Message); ok {
					ids = append(ids, m.GetID())
				}
			}
		}
		for sent := 0; sent < 300; {
			n := min(len(ids), 100)
			rnd := make([]int64, n)
			for i := range rnd {
				var b [8]byte
				_, _ = rand.Read(b[:])
				rnd[i] = int64(binary.LittleEndian.Uint64(b[:]))
			}
			if _, err := raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
				FromPeer: ch, ID: ids[:n], RandomID: rnd, ToPeer: group}); err != nil {
				return fmt.Errorf("forward: %w", err)
			}
			sent += n
			time.Sleep(time.Second)
		}
		if _, err := raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: group, ID: edit, Message: "own gap: edited"}); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		if _, err := raw.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{del}}); err != nil {
			return fmt.Errorf("delete: %w", err)
		}
		s1, err := raw.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		t.Logf("gap: pts %d -> %d (%d); edited %d, deleted %d", s0.Pts, s1.Pts, s1.Pts-s0.Pts, edit, del)

		page := func(name string, req *tg.UpdatesGetDifferenceRequest) tg.UpdatesDifferenceClass {
			d, err := raw.UpdatesGetDifference(ctx, req)
			if err != nil {
				t.Logf("%s: %v", name, err)
				return nil
			}
			switch x := d.(type) {
			case *tg.UpdatesDifferenceSlice:
				t.Logf("%s: slice new=%d other=%d next pts=%d", name, len(x.NewMessages), len(x.OtherUpdates), x.IntermediateState.Pts)
			case *tg.UpdatesDifference:
				kinds := map[string]int{}
				for _, u := range x.OtherUpdates {
					kinds[fmt.Sprintf("%T", u)]++
				}
				t.Logf("%s: difference new=%d other=%v pts=%d", name, len(x.NewMessages), kinds, x.State.Pts)
			case *tg.UpdatesDifferenceTooLong:
				t.Logf("%s: TOO LONG pts=%d", name, x.Pts)
			default:
				t.Logf("%s: %T", name, d)
			}
			return d
		}
		st := tg.UpdatesGetDifferenceRequest{Pts: s0.Pts, Date: s0.Date, Qts: s0.Qts}
		for calls := 1; calls <= 10; calls++ {
			req := st
			d := page(fmt.Sprintf("paging #%d", calls), &req)
			sl, ok := d.(*tg.UpdatesDifferenceSlice)
			if !ok {
				break
			}
			st = tg.UpdatesGetDifferenceRequest{Pts: sl.IntermediateState.Pts, Date: sl.IntermediateState.Date, Qts: sl.IntermediateState.Qts}
		}
		limited := tg.UpdatesGetDifferenceRequest{Pts: s0.Pts, Date: s0.Date, Qts: s0.Qts}
		limited.SetPtsTotalLimit(100)
		page("pts_total_limit 100", &limited)
		limited = tg.UpdatesGetDifferenceRequest{Pts: s0.Pts, Date: s0.Date, Qts: s0.Qts}
		limited.SetPtsTotalLimit(1000)
		page("pts_total_limit 1000", &limited)
		limited = tg.UpdatesGetDifferenceRequest{Pts: s0.Pts, Date: s0.Date, Qts: s0.Qts}
		limited.SetPtsLimit(50)
		page("pts_limit 50", &limited)
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
