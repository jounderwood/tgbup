package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
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

func TestSaveBatchStoresPeerNames(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	for _, title := range []string{"Новости", "Новости 2.0"} {
		b := Batch{ChatID: chatA, Peers: []Peer{{ID: -1000000000008, Title: title, Username: "news"}}}
		if err := s.SaveBatch(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	p, ok, err := s.PeerName(ctx, -1000000000008)
	if err != nil || !ok || p.Title != "Новости 2.0" || p.Username != "news" {
		t.Fatalf("peer = %+v %v %v", p, ok, err)
	}
	if ids, _ := s.ActiveChatIDs(ctx); len(ids) != 1 {
		t.Fatalf("peer names must not become archived chats: %v", ids)
	}
}

// A message read again from Telegram replaces the archived one when it
// changed: an edit keeps the version before, reactions are just updated, and
// a message that did not change is only marked as checked.
func TestSaveBatchKeepsEditsAndTheirEarlierVersions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	save := func(m Message) SaveStats {
		t.Helper()
		st, err := s.SaveBatchStats(ctx, Batch{ChatID: chatA, Messages: []Message{m}, Cursor: Cursor{NewestID: 1, OldestID: 1},
			Media: []Media{{Key: "photo:1", Kind: "photo"}, {Key: "photo:2", Kind: "photo"}}})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	row := func() (text, reacts string, edit int64, changed sql.NullInt64) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT text, COALESCE(reactions_json, ''), COALESCE(edit_date, 0), changed_at FROM messages
WHERE chat_id = ? AND id = 1`, chatA).Scan(&text, &reacts, &edit, &changed); err != nil {
			t.Fatal(err)
		}
		return
	}
	first := msg(1)
	first.Text, first.MediaKeys = "original", []string{"photo:1"}
	if st := save(first); st != (SaveStats{New: 1}) {
		t.Fatalf("first save = %+v", st)
	}
	// Read again, unchanged: nothing to do.
	if st := save(first); st != (SaveStats{}) {
		t.Fatalf("unchanged = %+v", st)
	}
	if _, _, _, changed := row(); changed.Valid {
		t.Fatal("an unchanged message counts as changed")
	}
	// Reactions only: updated in place, no version.
	reacted := first
	reacted.ReactionsJSON = `[{"emoji":"👍","count":2}]`
	if st := save(reacted); st != (SaveStats{Updated: 1}) {
		t.Fatalf("reactions = %+v", st)
	}
	if _, reacts, _, changed := row(); reacts != reacted.ReactionsJSON || !changed.Valid {
		t.Fatalf("reactions not kept: %q %v", reacts, changed)
	}
	// Edited, with the photo replaced: the archive has the new one, and the
	// version before with its photo.
	edited := reacted
	edited.Text, edited.EditDate, edited.MediaKeys = "edited", 1700000100, []string{"photo:2"}
	if st := save(edited); st != (SaveStats{Edited: 1}) {
		t.Fatalf("edit = %+v", st)
	}
	if text, _, edit, _ := row(); text != "edited" || edit != 1700000100 {
		t.Fatalf("after the edit: %q %d", text, edit)
	}
	if keys, _ := mediaKeysOf(t, s, chatA, 1); strings.Join(keys, ",") != "photo:2" {
		t.Errorf("media after the edit = %v", keys)
	}
	var (
		n             int
		vtext, vmedia string
		vedit         sql.NullInt64
	)
	if err := s.db.QueryRow(`SELECT n, text, media_keys, edit_date FROM message_versions WHERE chat_id = ? AND id = 1`, chatA).
		Scan(&n, &vtext, &vmedia, &vedit); err != nil {
		t.Fatal(err)
	}
	if n != 1 || vtext != "original" || vmedia != `["photo:1"]` || vedit.Valid {
		t.Errorf("version = %d %q %s %v", n, vtext, vmedia, vedit)
	}
	// Formatting stored another way, with no new edit time, is no edit.
	reformatted := edited
	reformatted.EntitiesJSON = `[{"type":"messageEntityBold","offset":0,"length":1}]`
	if st := save(reformatted); st != (SaveStats{Updated: 1}) {
		t.Fatalf("reformatted = %+v", st)
	}
	// A second edit makes version 2.
	again := reformatted
	again.Text, again.EditDate = "edited twice", 1700000200
	if st := save(again); st != (SaveStats{Edited: 1}) {
		t.Fatalf("second edit = %+v", st)
	}
	if count(t, s, `SELECT count(*) FROM message_versions WHERE chat_id = ? AND id = 1`, chatA) != 2 {
		t.Error("the second edit kept no version")
	}
}

func mediaKeysOf(t *testing.T, s *Store, chat int64, id int) ([]string, error) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return mediaKeys(context.Background(), tx, chat, id)
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
	// Upsert refreshes metadata but must not reactivate: only a successful sync does.
	if c.Status != ChatInaccessible || c.Type != ChatForum || c.Title != "B" || c.MigratedFromID != -42 || !slices.Equal(c.Folders, []string{"Семья"}) {
		t.Fatalf("chat = %+v", c)
	}
	if err := s.MarkActive(ctx, chatA); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := s.Chat(ctx, chatA); c.Status != ChatActive {
		t.Fatalf("status after MarkActive = %s", c.Status)
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

// Without an edit, nothing the archive has is lost: an attachment that went
// (a self-destructing photo, once opened) stays linked with its raw message,
// a preview picture the archive lacked is added, and a link preview Telegram
// sends poorer (not filled yet, dropped) is kept. None of it is a version.
func TestRereadWithoutAnEditLosesNothing(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	save := func(m Message) SaveStats {
		t.Helper()
		st, err := s.SaveBatchStats(ctx, Batch{ChatID: chatA, Messages: []Message{m},
			Media: []Media{{Key: "photo:1", Kind: "photo"}, {Key: "photo:9", Kind: "photo"}}})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	versions := func() int {
		return count(t, s, `SELECT count(*) FROM message_versions WHERE chat_id = ?`, chatA)
	}
	// A photo that self-destructs once opened: later Telegram sends it empty.
	photo := msg(1)
	photo.MediaKeys, photo.Raw = []string{"photo:1"}, []byte("raw with the photo")
	save(photo)
	expired := msg(1)
	expired.Raw = []byte("raw without it")
	if st := save(expired); st.Edited != 0 {
		t.Errorf("an expired photo counts as an edit: %+v", st)
	}
	if keys, _ := mediaKeysOf(t, s, chatA, 1); strings.Join(keys, ",") != "photo:1" {
		t.Errorf("the photo was unlinked: %v", keys)
	}
	var raw []byte
	_ = s.db.QueryRow(`SELECT raw FROM messages WHERE chat_id = ? AND id = 1`, chatA).Scan(&raw)
	if string(raw) != "raw with the photo" {
		t.Errorf("raw = %q", raw)
	}

	// A link preview archived before tgbup kept its picture.
	link := msg(2)
	link.Text, link.MetaJSON = "see example.com", `{"webpage":{"url":"https://example.com","title":"Example","description":"x"}}`
	save(link)
	withPicture := link
	withPicture.MediaKeys = []string{"photo:9"}
	if st := save(withPicture); st != (SaveStats{Updated: 1}) {
		t.Errorf("a preview picture added = %+v", st)
	}
	if keys, _ := mediaKeysOf(t, s, chatA, 2); strings.Join(keys, ",") != "photo:9" {
		t.Errorf("the preview picture was not linked: %v", keys)
	}
	// Telegram sends the preview not filled yet, or without it: the archive
	// keeps the full one, and the same answer again changes nothing.
	pending := withPicture
	pending.MetaJSON = `{"webpage":{"url":"https://example.com"}}`
	save(pending)
	if st := save(pending); st != (SaveStats{}) {
		t.Errorf("the same poorer preview again = %+v", st)
	}
	var meta string
	_ = s.db.QueryRow(`SELECT meta_json FROM messages WHERE chat_id = ? AND id = 2`, chatA).Scan(&meta)
	if !strings.Contains(meta, `"title":"Example"`) {
		t.Errorf("the full preview was lost: %s", meta)
	}
	if versions() != 0 {
		t.Errorf("%d versions without an edit", versions())
	}
}

// An edit keeps the meta of before (a checklist's items), and a live
// location's moves, edits by Telegram's clock, make no versions.
func TestVersionsKeepMetaButNotLiveLocations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	save := func(m Message) SaveStats {
		t.Helper()
		st, err := s.SaveBatchStats(ctx, Batch{ChatID: chatA, Messages: []Message{m}})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	list := msg(1)
	list.MetaJSON = `{"todo":{"title":"Shop","items":[{"id":1,"title":"milk"}]}}`
	save(list)
	renamed := list
	renamed.MetaJSON, renamed.EditDate = `{"todo":{"title":"Shopping","items":[{"id":1,"title":"milk"}]}}`, 1700000100
	if st := save(renamed); st != (SaveStats{Edited: 1}) {
		t.Fatalf("a renamed checklist = %+v", st)
	}
	var meta string
	_ = s.db.QueryRow(`SELECT meta_json FROM message_versions WHERE chat_id = ? AND id = 1`, chatA).Scan(&meta)
	if !strings.Contains(meta, `"title":"Shop"`) {
		t.Errorf("the version lost the checklist before: %q", meta)
	}
	live := msg(2)
	live.MetaJSON = `{"geo":{"lat":1,"long":2},"live":{"period":900}}`
	save(live)
	moved := live
	moved.MetaJSON, moved.EditDate = `{"geo":{"lat":1.5,"long":2},"live":{"period":900}}`, 1700000200
	if st := save(moved); st != (SaveStats{Updated: 1}) {
		t.Errorf("a live location's move = %+v", st)
	}
}
