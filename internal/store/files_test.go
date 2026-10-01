package store

import (
	"context"
	"testing"
)

func TestDownloadedFiles(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	const forum, old = int64(-1002263466511), int64(-4510946492)
	if err := s.UpsertChat(ctx, Chat{ID: forum, Type: ChatForum, Title: "Family", MigratedFromID: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChat(ctx, Chat{ID: old, Type: ChatGroup, Title: "Family"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTopics(ctx, []Topic{{ChatID: forum, TopicID: 160, Title: "Expenses"}}); err != nil {
		t.Fatal(err)
	}
	save := func(chat int64, m Message, media ...Media) {
		t.Helper()
		if err := s.SaveBatch(ctx, Batch{ChatID: chat, Messages: []Message{m}, Media: media}); err != nil {
			t.Fatal(err)
		}
	}
	doc := Media{Key: "document:9", Kind: "document", FileName: "Договор.pdf"}
	save(forum, Message{ChatID: forum, ID: 5, TopicID: 160, Date: 1710000000, Raw: []byte{1}, MediaKeys: []string{"document:9"}}, doc)
	save(old, Message{ChatID: old, ID: 2, Date: 1700000000, Raw: []byte{1}, MediaKeys: []string{"document:9"}}) // same file forwarded
	save(forum, Message{ChatID: forum, ID: 6, Date: 1710000100, Raw: []byte{1}, MediaKeys: []string{"photo:3"}}, Media{Key: "photo:3", Kind: "photo"})
	if err := s.MediaDone(ctx, "document:9", "media/document/9.pdf"); err != nil {
		t.Fatal(err)
	}

	got, err := s.DownloadedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []DownloadedFile{
		{LocalPath: "media/document/9.pdf", Kind: "document", FileName: "Договор.pdf", Date: 1700000000,
			ChatID: old, ChatTitle: "Family", ChatType: ChatGroup, UpgradedTo: forum, UpgradedTitle: "Family"},
		{LocalPath: "media/document/9.pdf", Kind: "document", FileName: "Договор.pdf", Date: 1710000000,
			ChatID: forum, ChatTitle: "Family", ChatType: ChatForum, TopicID: 160, TopicTitle: "Expenses"},
	}
	if len(got) != len(want) {
		t.Fatalf("files = %+v (the pending photo must be left out)", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %+v\nwant %+v", i, got[i], want[i])
		}
	}
}
