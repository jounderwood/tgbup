package store

import (
	"context"
	"testing"
)

func TestApplyTopicExclusions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const chat = -1001
	if err := s.UpsertChat(ctx, Chat{ID: chat, Type: ChatForum, Title: "F"}); err != nil {
		t.Fatal(err)
	}
	apply := func(excluded []int) bool {
		t.Helper()
		reset, err := s.ApplyTopicExclusions(ctx, chat, excluded, []int{1, 5, 7, 12})
		if err != nil {
			t.Fatal(err)
		}
		return reset
	}

	if apply([]int{5}) {
		t.Fatal("first exclusions without a cursor reset something")
	}
	if err := s.SaveBatch(ctx, Batch{ChatID: chat, Cursor: Cursor{NewestID: 100, OldestID: 1, BackfillDone: true}}); err != nil {
		t.Fatal(err)
	}
	if apply([]int{12, 5}) {
		t.Fatal("excluding one more topic must not re-read history")
	}
	if apply([]int{5, 12}) {
		t.Fatal("the same set in another order reset the cursor")
	}
	if !apply([]int{12}) {
		t.Fatal("re-including topic 5 did not reset backfill")
	}
	cur, _, err := s.GetCursor(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	if cur != (Cursor{NewestID: 100, OldestID: 101, BackfillDone: false}) {
		t.Fatalf("cursor after reset = %+v", cur)
	}
	if apply([]int{12}) {
		t.Fatal("unchanged exclusions reset the cursor again")
	}
}

// A deleted topic dropping out of the exclusions has nothing left to re-read.
func TestApplyTopicExclusionsIgnoresDeletedTopics(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const chat = -1001
	if err := s.UpsertChat(ctx, Chat{ID: chat, Type: ChatForum, Title: "F"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBatch(ctx, Batch{ChatID: chat, Cursor: Cursor{NewestID: 100, OldestID: 1, BackfillDone: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyTopicExclusions(ctx, chat, []int{5, 99}, []int{1, 5, 7, 99}); err != nil {
		t.Fatal(err)
	}
	// Topic 99 was deleted in Telegram; the user excludes 7 as well.
	reset, err := s.ApplyTopicExclusions(ctx, chat, []int{5, 7}, []int{1, 5, 7})
	if err != nil || reset {
		t.Fatalf("reset = %v %v: a deleted topic triggered a history re-read", reset, err)
	}
	if cur, _, _ := s.GetCursor(ctx, chat); !cur.BackfillDone {
		t.Fatalf("cursor = %+v", cur)
	}
}
