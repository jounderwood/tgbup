package store

import (
	"context"
	"maps"
	"reflect"
	"testing"
)

func TestFeedStateIsKeptPerBox(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, ok, err := s.Feed(ctx, 0); ok || err != nil {
		t.Fatalf("a new archive follows a feed: %v %v", ok, err)
	}
	if err := s.StartFeed(ctx, Feed{Box: 0, Pts: 10, Qts: 2, Date: 1700000000}); err != nil {
		t.Fatal(err)
	}
	// Started already: a second start does not move it back.
	if err := s.SetFeed(ctx, Feed{Box: 0, Pts: 15, Qts: 2, Date: 1700000100, ReadAt: 1700000200}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartFeed(ctx, Feed{Box: 0, Pts: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartFeed(ctx, Feed{Box: chatA, Pts: 7}); err != nil {
		t.Fatal(err)
	}
	if f, ok, err := s.Feed(ctx, 0); !ok || err != nil || f != (Feed{Box: 0, Pts: 15, Qts: 2, Date: 1700000100, ReadAt: 1700000200}) {
		t.Fatalf("own feed = %+v %v %v", f, ok, err)
	}
	if all, err := s.Feeds(ctx); err != nil || len(all) != 2 || all[0].Pts != 15 || all[chatA] != (Feed{Box: chatA, Pts: 7}) {
		t.Fatalf("Feeds = %+v %v", all, err)
	}
	if f, _, _ := s.Feed(ctx, chatA); f.Pts != 7 {
		t.Fatalf("channel feed = %+v", f)
	}
	if err := s.ForgetFeed(ctx, chatA); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Feed(ctx, chatA); ok {
		t.Fatal("a forgotten feed is still followed")
	}
}

// An id in the account's own feed names a message in a private chat or a
// basic group, never one of a channel, whose ids are its own.
func TestCommonChatsFindsAnIdOutsideChannels(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	const private, group = int64(42), int64(-77)
	for _, c := range []Chat{{ID: private, Type: ChatPrivate}, {ID: group, Type: ChatGroup}, {ID: chatA, Type: ChatSupergroup}} {
		if err := s.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	save := func(chat int64, ids ...int) {
		t.Helper()
		var ms []Message
		for _, id := range ids {
			m := msg(id)
			m.ChatID = chat
			ms = append(ms, m)
		}
		if err := s.SaveBatch(ctx, Batch{ChatID: chat, Messages: ms}); err != nil {
			t.Fatal(err)
		}
	}
	save(private, 10, 11, 12)
	save(group, 20, 21)
	save(chatA, 10, 11, 20) // the same numbers in a channel
	if _, err := s.MarkDeleted(ctx, private, []int{12}, 1); err != nil {
		t.Fatal(err)
	}

	got, err := s.CommonChats(ctx, []int{10, 12, 20, 99})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64][]int{private: {10}, group: {20}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CommonChats = %v, want %v", got, want)
	}

	edited := msg(11)
	edited.ChatID, edited.Text, edited.EditDate = private, "fixed", 1700005000
	if err := s.SaveBatch(ctx, Batch{ChatID: private, Messages: []Message{edited}}); err != nil {
		t.Fatal(err)
	}
	has, err := s.MessageStates(ctx, private, []int{10, 11, 12, 13})
	if err != nil {
		t.Fatal(err)
	}
	want2 := map[int]MessageState{10: {}, 11: {EditDate: 1700005000}, 12: {Deleted: true}}
	if !maps.Equal(has, want2) {
		t.Fatalf("MessageStates = %v, want %v", has, want2)
	}
	var many []int
	for i := range 1200 {
		many = append(many, 1000+i)
	}
	if has, err := s.MessageStates(ctx, private, append(many, 10)); err != nil || !maps.Equal(has, map[int]MessageState{10: {}}) {
		t.Fatalf("MessageStates over many ids = %v %v", has, err)
	}
}
