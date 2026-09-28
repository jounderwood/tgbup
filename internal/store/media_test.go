package store

import (
	"context"
	"testing"
)

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
		if err := s.MediaAttemptFailed(ctx, "document:1", "boom", 5); err != nil {
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
