package store

import (
	"context"
	"slices"
	"testing"
)

const chatA = int64(-1000000000001)

func seedChat(t *testing.T, s *Store, id int64) {
	t.Helper()
	if err := s.UpsertChat(context.Background(), Chat{ID: id, Type: ChatSupergroup, Title: "A"}); err != nil {
		t.Fatal(err)
	}
}

func msg(id int, keys ...string) Message {
	return Message{ChatID: chatA, ID: id, Date: 1700000000 + int64(id), Text: "t", Raw: []byte{1}, TLLayer: 1, MediaKeys: keys}
}

func TestSaveBatchWritesEverything(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)

	err := s.SaveBatch(ctx, Batch{
		ChatID:   chatA,
		Users:    []User{{ID: 5, FirstName: "Ann", Username: "ann"}},
		Media:    []Media{{Key: "document:9", Kind: "document", FileName: "a.pdf", Mime: "application/pdf", Size: 10}},
		Messages: []Message{msg(1), msg(2, "document:9")},
		Cursor:   Cursor{NewestID: 2, OldestID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountMessages(ctx, chatA); n != 2 {
		t.Errorf("messages = %d", n)
	}
	if count(t, s, `SELECT count(*) FROM message_media WHERE media_key='document:9'`) != 1 {
		t.Error("message_media not written")
	}
	if count(t, s, `SELECT count(*) FROM media WHERE key='document:9' AND status='pending'`) != 1 {
		t.Error("media not pending")
	}
	cur, ok, err := s.GetCursor(ctx, chatA)
	if err != nil || !ok || cur != (Cursor{NewestID: 2, OldestID: 1}) {
		t.Fatalf("cursor = %+v %v %v", cur, ok, err)
	}
	if count(t, s, `SELECT count(*) FROM chats WHERE id=? AND last_synced_at IS NOT NULL`, chatA) != 1 {
		t.Error("last_synced_at not set")
	}
}

func TestSaveBatchKeepsFirstVersionOfMessage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	first := msg(1)
	first.Text = "original"
	edited := msg(1)
	edited.Text = "edited"

	for _, m := range []Message{first, edited} {
		if err := s.SaveBatch(ctx, Batch{ChatID: chatA, Messages: []Message{m}, Cursor: Cursor{NewestID: 1, OldestID: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	var text string
	if err := s.db.QueryRow(`SELECT text FROM messages WHERE chat_id=? AND id=1`, chatA).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "original" {
		t.Fatalf("text = %q", text)
	}
}

func TestSaveBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)

	// media key is not in Batch.Media → FK violation on message_media → whole batch must roll back.
	err := s.SaveBatch(ctx, Batch{
		ChatID:   chatA,
		Users:    []User{{ID: 5}},
		Messages: []Message{msg(1), msg(2, "document:404")},
		Cursor:   Cursor{NewestID: 2, OldestID: 1},
	})
	if err == nil {
		t.Fatal("expected FK error")
	}
	if n, _ := s.CountMessages(ctx, chatA); n != 0 {
		t.Errorf("messages = %d after failed batch", n)
	}
	if count(t, s, `SELECT count(*) FROM users`) != 0 {
		t.Error("users written by failed batch")
	}
	if _, ok, _ := s.GetCursor(ctx, chatA); ok {
		t.Error("cursor moved by failed batch")
	}
}

func TestPartialUserDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	full := Batch{ChatID: chatA, Users: []User{{ID: 5, FirstName: "Ann", Username: "ann"}}}
	partial := Batch{ChatID: chatA, Users: []User{{ID: 5, Partial: true}, {ID: 6, FirstName: "New", Partial: true}}}
	for _, b := range []Batch{full, partial} {
		if err := s.SaveBatch(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, s, `SELECT count(*) FROM users WHERE id=5 AND username='ann'`) != 1 {
		t.Error("partial user overwrote full one")
	}
	if count(t, s, `SELECT count(*) FROM users WHERE id=6`) != 1 {
		t.Error("partial user not inserted when absent")
	}
}

func TestSetPinnedReplacesPreviousSet(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	if err := s.SaveBatch(ctx, Batch{ChatID: chatA, Messages: []Message{msg(1), msg(2), msg(3)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, chatA, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, chatA, []int{3, 99}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PinnedMessageIDs(ctx, chatA)
	if err != nil || !slices.Equal(got, []int{3}) {
		t.Fatalf("pinned = %v %v", got, err)
	}
}

func TestChatStatusLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)

	changed, err := s.MarkInaccessible(ctx, chatA)
	if err != nil || !changed {
		t.Fatalf("first mark: %v %v", changed, err)
	}
	changed, _ = s.MarkInaccessible(ctx, chatA)
	if changed {
		t.Fatal("second mark reported change")
	}
	ids, _ := s.ActiveChatIDs(ctx)
	if len(ids) != 0 {
		t.Fatalf("active = %v", ids)
	}

	if err := s.UpsertChat(ctx, Chat{ID: chatA, Type: ChatForum, Title: "B", Folders: []string{"Семья"}, MigratedFromID: -42}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.Chat(ctx, chatA)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if c.Status != ChatActive || c.Type != ChatForum || c.Title != "B" || c.MigratedFromID != -42 || !slices.Equal(c.Folders, []string{"Семья"}) {
		t.Fatalf("chat = %+v", c)
	}
}

func TestUpsertTopics(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	if err := s.UpsertTopics(ctx, []Topic{{ChatID: chatA, TopicID: 1, Title: "General"}, {ChatID: chatA, TopicID: 7, Title: "Old"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTopics(ctx, []Topic{{ChatID: chatA, TopicID: 7, Title: "Renamed", Closed: true}}); err != nil {
		t.Fatal(err)
	}
	ts, err := s.Topics(ctx, chatA)
	if err != nil || len(ts) != 2 || ts[1].Title != "Renamed" || !ts[1].Closed {
		t.Fatalf("topics = %+v %v", ts, err)
	}
}
