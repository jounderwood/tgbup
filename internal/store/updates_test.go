package store

import (
	"context"
	"testing"
	"time"
)

func TestNoteUpdate(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t0 := time.Unix(1772400000, 0)
	if _, created, ok, err := s.NoteUpdate(ctx, 0, t0); err != nil || created || ok {
		t.Fatalf("empty archive: created=%v ok=%v err=%v", created, ok, err)
	}
	if err := s.UpsertChat(ctx, Chat{ID: 10, Type: ChatPrivate, Title: "Mom"}); err != nil {
		t.Fatal(err)
	}
	save := func(ids ...int) {
		t.Helper()
		var ms []Message
		for _, id := range ids {
			ms = append(ms, Message{ChatID: 10, ID: id, Date: 1772300000, Raw: []byte{1}})
		}
		if err := s.SaveBatch(ctx, Batch{ChatID: 10, Messages: ms}); err != nil {
			t.Fatal(err)
		}
	}
	save(1, 2)
	u, created, ok, err := s.NoteUpdate(ctx, 7, t0)
	if err != nil || !created || !ok || u.N != 1 || !u.Diff.First || u.Diff.Messages != 2 || u.Diff.Pages != 0 {
		t.Fatalf("first: %+v created=%v ok=%v err=%v", u, created, ok, err)
	}
	// Nothing new: no update, the latest is returned.
	u, created, _, _ = s.NoteUpdate(ctx, 0, t0.Add(time.Hour))
	if created || u.N != 1 {
		t.Fatalf("unchanged: %+v created=%v", u, created)
	}
	// A redrawn page alone is a change (an edit or a reaction).
	if u, created, _, _ = s.NoteUpdate(ctx, 2, t0.Add(2*time.Hour)); !created || u.N != 2 || u.Diff.Pages != 2 || u.Diff.Messages != 0 {
		t.Fatalf("pages: %+v", u)
	}
	save(3)
	u, created, _, _ = s.NoteUpdate(ctx, 1, t0.Add(3*time.Hour))
	if !created || u.N != 3 || u.Diff.Messages != 1 || u.Diff.Chats[10] != 1 || u.Diff.First {
		t.Fatalf("new message: %+v", u)
	}

	if err := s.AddSnapshot(ctx, "/b", "aaa", 2, false, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSnapshot(ctx, "/b", "bbb", 3, false, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if id, n, ok, _ := s.LastSnapshot(ctx, "/b"); !ok || id != "bbb" || n != 3 {
		t.Fatalf("last snapshot = %q %d %v", id, n, ok)
	}
	// A snapshot that missed files does not count as holding its update.
	if err := s.AddSnapshot(ctx, "/b", "ccc", 3, true, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if id, _, _, _ := s.LastSnapshot(ctx, "/b"); id != "bbb" {
		t.Fatalf("last snapshot after a partial one = %q", id)
	}
	if _, _, ok, _ := s.LastSnapshot(ctx, "/other"); ok {
		t.Fatal("a place without snapshots has a last one")
	}
	if err := s.ForgetSnapshots(ctx, "/b", []string{"aaa"}, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	us, err := s.Updates(ctx, 10)
	if err != nil || len(us) != 3 || us[0].N != 3 || us[2].N != 1 {
		t.Fatalf("updates = %+v, %v", us, err)
	}
	if len(us[0].Snapshots) != 2 || us[0].Snapshots[0].ID != "bbb" || !us[0].Snapshots[1].Partial || len(us[1].Snapshots) != 1 || !us[1].Snapshots[0].Forgotten {
		t.Fatalf("snapshots: %+v / %+v", us[0].Snapshots, us[1].Snapshots)
	}
	by, err := s.SnapshotsByUpdate(ctx)
	if err != nil || len(by[3]) != 2 || len(by[2]) != 0 {
		t.Fatalf("by update = %+v, %v (update 2's only snapshot was forgotten)", by, err)
	}
	m, err := s.SnapshotUpdates(ctx, "/b")
	if err != nil || m["bbb"].N != 3 || m["aaa"].N != 2 {
		t.Fatalf("snapshot updates = %+v, %v", m, err)
	}
}

// An update counts what was found changed since the one before: edits (they
// kept a version) and messages with other reactions or votes.
func TestNoteUpdateCountsEditsAndReactions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.UpsertChat(ctx, Chat{ID: 10, Type: ChatPrivate, Title: "Mom"}); err != nil {
		t.Fatal(err)
	}
	save := func(ms ...Message) {
		t.Helper()
		for i := range ms {
			ms[i].ChatID, ms[i].Date, ms[i].Raw = 10, 1700000000, []byte{1}
		}
		if err := s.SaveBatch(ctx, Batch{ChatID: 10, Messages: ms}); err != nil {
			t.Fatal(err)
		}
	}
	save(Message{ID: 1, Text: "a"}, Message{ID: 2, Text: "b"})
	t0 := time.Now().Add(-time.Minute)
	if _, _, _, err := s.NoteUpdate(ctx, 0, t0); err != nil {
		t.Fatal(err)
	}
	save(Message{ID: 1, Text: "a, fixed", EditDate: 1700000100}, Message{ID: 2, Text: "b", ReactionsJSON: `[{"emoji":"👍","count":1}]`})
	u, created, _, err := s.NoteUpdate(ctx, 0, time.Now())
	if err != nil || !created || u.Diff.Edited != 1 || u.Diff.Updated != 1 || u.Diff.Messages != 0 {
		t.Fatalf("update = %+v created=%v err=%v", u.Diff, created, err)
	}
	if got := u.Diff.Summary(); got != "1 edited · 1 with new reactions or votes" {
		t.Errorf("summary = %q", got)
	}
}
