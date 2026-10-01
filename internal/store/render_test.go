package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// renderFixture: a forum with General and topic 160 across two months, a user and a channel sender.
func renderFixture(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	const forum = int64(-1001)
	if err := s.UpsertChat(ctx, Chat{ID: forum, Type: ChatForum, Title: "Family", MigratedFromID: -45}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTopics(ctx, []Topic{{ChatID: forum, TopicID: 1, Title: "General"}, {ChatID: forum, TopicID: 160, Title: "Expenses"}}); err != nil {
		t.Fatal(err)
	}
	feb := time.Date(2026, 2, 28, 23, 30, 0, 0, time.UTC).Unix()
	mar := time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC).Unix()
	err := s.SaveBatch(ctx, Batch{
		ChatID: forum,
		Users:  []User{{ID: 5, FirstName: "Ann", LastName: "Lee"}},
		Peers:  []Peer{{ID: -1000000000008, Title: "News"}},
		Media:  []Media{{Key: "document:9", Kind: "document", FileName: "lease.pdf", Mime: "application/pdf", Size: 2048}},
		Messages: []Message{
			{ChatID: forum, ID: 10, TopicID: 1, Date: feb, FromID: 5, Text: "hello", Raw: []byte{1}},
			{ChatID: forum, ID: 11, TopicID: 160, Date: mar, FromID: 5, Text: "rent", Raw: []byte{1}, MediaKeys: []string{"document:9"},
				MetaJSON: `{"geo":{"lat":1,"long":2}}`},
			{ChatID: forum, ID: 12, TopicID: 160, Date: mar + 60, FromID: -1000000000008, Text: "news", ReplyToID: 10, Raw: []byte{1},
				FwdFromJSON: `{"from_id":5,"date":1}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPagesAndFingerprints(t *testing.T) {
	s := renderFixture(t)
	ctx := context.Background()
	pages, err := s.Pages(ctx, PageQuery{Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	type pg struct {
		topic, n, total int
		ids             []int
	}
	var got []pg
	for _, p := range pages {
		got = append(got, pg{p.TopicID, p.N, p.Total, p.IDs})
	}
	want := []pg{{1, 1, 1, []int{10}}, {160, 1, 2, []int{11}}, {160, 2, 2, []int{12}}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
	if big, _ := s.Pages(ctx, PageQuery{Size: 500}); len(big) != 2 || len(big[1].IDs) != 2 {
		t.Fatalf("pages of 500 = %+v", big)
	}
	if only, _ := s.Pages(ctx, PageQuery{Size: 500, Topics: []int{160}, From: time.Date(2026, 3, 1, 0, 30, 30, 0, time.UTC).Unix()}); len(only) != 1 || fmt.Sprint(only[0].IDs) != "[12]" {
		t.Fatalf("filtered pages = %+v", only)
	}

	if err := s.MediaDone(ctx, "document:9", "media/document/9.pdf"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Pages(ctx, PageQuery{Size: 1})
	if after[1].Fingerprint == pages[1].Fingerprint || after[0].Fingerprint != pages[0].Fingerprint || after[2].Fingerprint != pages[2].Fingerprint {
		t.Fatal("a downloaded file must change only its own page")
	}

	if err := s.SetRendered(ctx, after[1].PageKey, "fp"); err != nil {
		t.Fatal(err)
	}
	done, err := s.RenderedFingerprints(ctx)
	if err != nil || done[after[1].PageKey] != "fp" || len(done) != 1 {
		t.Fatalf("rendered = %v %v", done, err)
	}
	if err := s.ClearRendered(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.RenderedFingerprints(ctx); len(done) != 0 {
		t.Fatalf("after clear = %v", done)
	}
}

func TestPageMessagesAndRefs(t *testing.T) {
	s := renderFixture(t)
	ctx := context.Background()
	msgs, err := s.PageMessages(ctx, -1001, []int{12, 11})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != 11 || msgs[0].SenderName != "Ann Lee" || msgs[1].SenderName != "News" || msgs[1].FwdName != "Ann Lee" {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].MetaJSON != `{"geo":{"lat":1,"long":2}}` {
		t.Errorf("meta = %q", msgs[0].MetaJSON)
	}
	if m := msgs[0].Media; m == nil || m.Key != "document:9" || m.FileName != "lease.pdf" || m.Status != "pending" || m.Size != 2048 {
		t.Fatalf("media = %+v", msgs[0].Media)
	}
	refs, err := s.MessageRefs(ctx, -1001, []int{10, 99})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := refs[10]; !ok || r.TopicID != 1 || r.SenderName != "Ann Lee" || r.Text != "hello" {
		t.Fatalf("ref = %+v", refs[10])
	}
	if _, ok := refs[99]; ok {
		t.Fatal("a message not in the archive has a ref")
	}
}

func TestRenderChats(t *testing.T) {
	s := renderFixture(t)
	chats, err := s.RenderChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Title != "Family" || chats[0].MigratedFromID != -45 || chats[0].Topics[160] != "Expenses" {
		t.Fatalf("chats = %+v", chats)
	}
}

func TestSearchTexts(t *testing.T) {
	s := renderFixture(t)
	got, err := s.SearchTexts(context.Background(), -1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Text != "hello" || got[1].Text != "rent lease.pdf" || got[1].TopicID != 160 || got[1].MetaJSON == "" {
		t.Fatalf("texts = %+v", got)
	}
}

func TestNames(t *testing.T) {
	s := renderFixture(t)
	got, err := s.Names(context.Background(), []int64{5, -1000000000008, -1001, 404})
	if err != nil {
		t.Fatal(err)
	}
	if got[5] != "Ann Lee" || got[-1000000000008] != "News" || got[-1001] != "Family" || got[404] != "" {
		t.Fatalf("names = %v", got)
	}
}
