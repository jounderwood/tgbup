package store

import (
	"context"
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
			{ChatID: forum, ID: 11, TopicID: 160, Date: mar, FromID: 5, Text: "rent", Raw: []byte{1}, MediaKeys: []string{"document:9"}},
			{ChatID: forum, ID: 12, TopicID: 160, Date: mar + 60, FromID: -1000000000008, Text: "news", ReplyToID: 10, Raw: []byte{1},
				FwdFromJSON: `{"from_id":5,"date":1}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMonthsAndFingerprints(t *testing.T) {
	s := renderFixture(t)
	ctx := context.Background()
	months, err := s.Months(ctx, nil, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(months) != 2 || months[0].Month != "2026-02" || months[0].TopicID != 1 || months[1].Month != "2026-03" || months[1].TopicID != 160 {
		t.Fatalf("months = %+v", months)
	}
	// Months follow the given time zone: 23:30 UTC on Feb 28 is March 1 in Belgrade.
	bel, _ := time.LoadLocation("Europe/Belgrade")
	if m, _ := s.Months(ctx, nil, bel); m[0].Month != "2026-03" {
		t.Fatalf("months in Belgrade = %+v", m)
	}

	before := months[1].Fingerprint
	if err := s.MediaDone(ctx, "document:9", "media/document/9.pdf"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Months(ctx, nil, time.UTC)
	if after[1].Fingerprint == before || after[0].Fingerprint != months[0].Fingerprint {
		t.Fatalf("a downloaded file must change only its month: %q→%q, %q→%q",
			before, after[1].Fingerprint, months[0].Fingerprint, after[0].Fingerprint)
	}

	if err := s.SetRendered(ctx, after[1]); err != nil {
		t.Fatal(err)
	}
	done, err := s.RenderedFingerprints(ctx)
	if err != nil || done[after[1].Page()] != after[1].Fingerprint || len(done) != 1 {
		t.Fatalf("rendered = %v %v", done, err)
	}
}

func TestMonthMessagesAndRefs(t *testing.T) {
	s := renderFixture(t)
	ctx := context.Background()
	msgs, err := s.MonthMessages(ctx, -1001, 160, "2026-03", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != 11 || msgs[0].SenderName != "Ann Lee" || msgs[1].SenderName != "News" || msgs[1].FwdName != "Ann Lee" {
		t.Fatalf("messages = %+v", msgs)
	}
	if m := msgs[0].Media; m == nil || m.FileName != "lease.pdf" || m.Status != "pending" || m.Size != 2048 {
		t.Fatalf("media = %+v", msgs[0].Media)
	}
	refs, err := s.MessageRefs(ctx, -1001, []int{10, 99}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := refs[10]; !ok || r.TopicID != 1 || r.Month != "2026-02" || r.SenderName != "Ann Lee" || r.Text != "hello" {
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
