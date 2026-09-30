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
		reset, err := s.ApplyTopicExclusions(ctx, chat, excluded)
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
