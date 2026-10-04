package store

import (
	"context"
	"testing"
)

func TestPendingMediaPairsChatAndMessageFromOneLink(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	const private = int64(12345)
	seedChat(t, s, chatA)
	if err := s.UpsertChat(ctx, Chat{ID: private, Type: ChatPrivate}); err != nil {
		t.Fatal(err)
	}
	priv := Message{ChatID: private, ID: 10, Date: 1, Raw: []byte{1}, TLLayer: 1, MediaKeys: []string{"photo:1"}}
	if err := s.SaveBatch(ctx, Batch{ChatID: private, Media: []Media{{Key: "photo:1", Kind: "photo"}}, Messages: []Message{priv}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBatch(ctx, Batch{ChatID: chatA, Media: []Media{{Key: "photo:1", Kind: "photo"}}, Messages: []Message{msg(500, "photo:1")}}); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.PendingMedia(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}
	if _, err := s.MessageRaw(ctx, jobs[0].ChatID, jobs[0].MessageID); err != nil {
		t.Fatalf("job points at a non-existent message %d/%d: %v", jobs[0].ChatID, jobs[0].MessageID, err)
	}
}

func TestMediaQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	err := s.SaveBatch(ctx, Batch{
		ChatID:   chatA,
		Media:    []Media{{Key: "document:1", Kind: "document", Size: 10}, {Key: "photo:2", Kind: "photo"}},
		Messages: []Message{msg(1, "document:1"), msg(2, "photo:2"), msg(3, "document:1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.PendingMedia(ctx)
	if err != nil || len(jobs) != 2 || jobs[0].Key != "document:1" || jobs[0].ChatID != chatA {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}

	for i := 0; i < 5; i++ {
		if _, err := s.MediaAttemptFailed(ctx, "document:1", "boom", 5); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := s.MediaInfo(ctx, "document:1")
	if info.Status != MediaFailed || info.Attempts != 5 || info.Error != "boom" {
		t.Fatalf("info = %+v", info)
	}
	if err := s.MediaDone(ctx, "photo:2", "media/photo/2.jpg"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.PendingMedia(ctx); len(jobs) != 0 {
		t.Fatalf("pending after done/failed = %+v", jobs)
	}

	if err := s.UpdateMessageRaw(ctx, chatA, 1, []byte{9, 9}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.MessageRaw(ctx, chatA, 1)
	if err != nil || len(raw) != 2 {
		t.Fatalf("raw = %v %v", raw, err)
	}
}

func TestReserveMediaPath(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	m := msg(1, "document:1", "document:2")
	m.Date = 1718388153
	if err := s.SaveBatch(ctx, Batch{ChatID: chatA,
		Media:    []Media{{Key: "document:1", Kind: "document"}, {Key: "document:2", Kind: "document"}},
		Messages: []Message{m}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ReserveMediaPath(ctx, "document:1", "media/2024/a.pdf", "media/2024/a.pdf"); !ok || err != nil {
		t.Fatalf("first reservation: %v %v", ok, err)
	}
	if ok, err := s.ReserveMediaPath(ctx, "document:2", "media/2024/A.pdf", "media/2024/a.pdf"); ok || err != nil {
		t.Fatalf("a taken name was given again: %v %v", ok, err)
	}
	jobs, err := s.PendingMedia(ctx)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}
	if j := jobs[0]; j.LocalPath != "media/2024/a.pdf" || j.Date != 1718388153 || j.ChatTitle != "A" {
		t.Errorf("job = %+v", j)
	}
	if jobs[1].LocalPath != "" {
		t.Errorf("unreserved job has a path: %+v", jobs[1])
	}
}
