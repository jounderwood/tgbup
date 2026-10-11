package syncer_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/syncer"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

type fakeTG struct {
	mu        sync.Mutex
	ids       map[int64][]int // chat → ascending message ids
	empty     map[int64]map[int]bool
	errs      map[int64]error
	edits     map[int64]map[int]string // chat → message → its text after an edit
	byID      int                      // Messages calls
	failAfter map[int64]int
	calls     map[int64]int
	before    int
	pinned    map[int64][]int
	topics    map[int64][]tgclient.Topic
	replyTop  map[int]int // message id → topic id (forum)
	// brokenAfter makes HistoryAfter return nothing, like a server that
	// interprets the forward-paging parameters differently.
	brokenAfter bool
}

func newFake() *fakeTG {
	return &fakeTG{ids: map[int64][]int{}, empty: map[int64]map[int]bool{}, errs: map[int64]error{}, edits: map[int64]map[int]string{},
		failAfter: map[int64]int{}, calls: map[int64]int{}, pinned: map[int64][]int{}, topics: map[int64][]tgclient.Topic{}, replyTop: map[int]int{}}
}

func (f *fakeTG) add(chat int64, from, to int) {
	for i := from; i <= to; i++ {
		f.ids[chat] = append(f.ids[chat], i)
	}
}

func key(p tg.InputPeerClass) int64 { id, _ := convert.InputPeerID(p); return id }

func (f *fakeTG) message(chat int64, id int) tg.MessageClass {
	if f.empty[chat][id] {
		return &tg.MessageEmpty{ID: id}
	}
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: -chat - 1000000000000}, Date: 1700000000 + id, Message: "m"}
	if text, ok := f.edits[chat][id]; ok {
		m.Message = text
		m.SetEditDate(1700009999)
	}
	m.SetFromID(&tg.PeerUser{UserID: 5})
	if top, ok := f.replyTop[id]; ok {
		h := &tg.MessageReplyHeader{ForumTopic: true}
		h.SetReplyToMsgID(top)
		m.SetReplyTo(h)
	}
	return m
}

func (f *fakeTG) check(chat int64) error {
	f.calls[chat]++
	if err := f.errs[chat]; err != nil {
		return err
	}
	if n, ok := f.failAfter[chat]; ok && f.calls[chat] > n {
		return errors.New("connection reset")
	}
	return nil
}

func (f *fakeTG) page(chat int64, ids []int) tgclient.Page {
	p := tgclient.Page{
		Users: []tg.UserClass{&tg.User{ID: 5, FirstName: "Ann"}},
		Chats: []tg.ChatClass{&tg.Channel{ID: 8, Title: "Новости"}},
	}
	for _, id := range ids {
		p.Messages = append(p.Messages, f.message(chat, id))
	}
	return p
}

func (f *fakeTG) HistoryBefore(_ context.Context, peer tg.InputPeerClass, beforeID, limit int) (tgclient.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chat := key(peer)
	f.before++
	if err := f.check(chat); err != nil {
		return tgclient.Page{}, err
	}
	var out []int
	all := f.ids[chat]
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if beforeID == 0 || all[i] < beforeID {
			out = append(out, all[i])
		}
	}
	return f.page(chat, out), nil
}

func (f *fakeTG) HistoryAfter(_ context.Context, peer tg.InputPeerClass, afterID, limit int) (tgclient.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chat := key(peer)
	if err := f.check(chat); err != nil {
		return tgclient.Page{}, err
	}
	var out []int
	if f.brokenAfter {
		return f.page(chat, out), nil
	}
	for _, id := range f.ids[chat] {
		if id > afterID && len(out) < limit {
			out = append(out, id)
		}
	}
	return f.page(chat, out), nil
}

func (f *fakeTG) PinnedIDs(_ context.Context, peer tg.InputPeerClass) ([]int, error) {
	return f.pinned[key(peer)], nil
}

func (f *fakeTG) ForumTopics(_ context.Context, peer tg.InputPeerClass) ([]tgclient.Topic, error) {
	return f.topics[key(peer)], nil
}

func (f *fakeTG) Message(context.Context, tg.InputPeerClass, int) (tg.MessageClass, error) {
	return nil, errors.New("not used")
}

// Messages gives the ones by id that the chat still has; a deleted one comes
// back empty, as Telegram does.
func (f *fakeTG) Messages(_ context.Context, peer tg.InputPeerClass, ids []int) (tgclient.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chat := key(peer)
	f.byID++
	var out tgclient.Page
	for _, id := range ids {
		if slices.Contains(f.ids[chat], id) {
			out.Messages = append(out.Messages, f.message(chat, id))
		} else {
			out.Messages = append(out.Messages, &tg.MessageEmpty{ID: id})
		}
	}
	return out, nil
}

// remove deletes messages from the chat, as in Telegram.
func (f *fakeTG) remove(chat int64, ids ...int) {
	f.ids[chat] = slices.DeleteFunc(f.ids[chat], func(id int) bool { return slices.Contains(ids, id) })
}

func (f *fakeTG) FileChunk(context.Context, tg.InputFileLocationClass, int64, int) ([]byte, error) {
	return nil, errors.New("not used")
}

func target(n int64, typ string) tgclient.Target {
	id := convert.ChannelID(n)
	return tgclient.Target{Chat: store.Chat{ID: id, Type: typ, Title: "chat"}, Peer: &tg.InputPeerChannel{ChannelID: n}}
}

type env struct {
	s   *syncer.Syncer
	st  *store.Store
	tg  *fakeTG
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// A month after the fake's messages: the recent window (refresh) is empty,
	// except in the tests about it.
	e := &env{st: st, tg: newFake(), now: time.Unix(1700000000, 0).Add(30 * 24 * time.Hour)}
	e.s = &syncer.Syncer{
		TG: e.tg, Store: st, SelfID: 1, Budget: time.Hour, PageSize: 100,
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AuditPages: -1, // off, except in the tests about it
	}
	return e
}

func dialogsOf(ts ...tgclient.Target) map[int64]bool {
	m := map[int64]bool{}
	for _, t := range ts {
		m[t.Chat.ID] = true
	}
	return m
}

func (e *env) count(t *testing.T, chat int64) int {
	t.Helper()
	n, err := e.st.CountMessages(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInitialSyncBackfillsEverything(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 250)

	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if !r.OK() || r.Chats != 1 {
		t.Fatalf("report = %+v", r)
	}
	if n := e.count(t, a.Chat.ID); n != 250 {
		t.Fatalf("messages = %d", n)
	}
	cur, _, _ := e.st.GetCursor(context.Background(), a.Chat.ID)
	if cur != (store.Cursor{NewestID: 250, OldestID: 1, BackfillDone: true}) {
		t.Fatalf("cursor = %+v", cur)
	}
	if p, ok, _ := e.st.PeerName(context.Background(), -1000000000008); !ok || p.Title != "Новости" {
		t.Fatalf("forward-source name not stored: %+v %v", p, ok)
	}
}

func TestIncrementalFetchesOnlyNew(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 120)
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))

	e.tg.add(a.Chat.ID, 121, 130)
	e.tg.before = 0
	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if r.NewMessages != 10 || e.count(t, a.Chat.ID) != 130 {
		t.Fatalf("new=%d total=%d", r.NewMessages, e.count(t, a.Chat.ID))
	}
	if e.tg.before != 0 {
		t.Fatalf("backfill called %d times after it was done", e.tg.before)
	}
}

func TestForwardSyncShortOfDialogTopMessageFails(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 120)
	a.TopMessage = 120
	if r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a)); !r.OK() {
		t.Fatalf("first run = %+v", r)
	}

	e.tg.add(a.Chat.ID, 121, 130)
	a.TopMessage = 130
	e.tg.brokenAfter = true
	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if r.Failed[a.Chat.ID] == nil {
		t.Fatalf("silent gap: forward sync stopped at 120 while the dialog ends at 130; report = %+v", r)
	}
}

func TestBudgetStopsBackfillAndNextRunResumes(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 450)
	e.s.Budget = 0 // deadline already reached: the first page still can't be fetched
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 0 {
		t.Fatalf("messages with zero budget = %d", n)
	}

	calls := 0
	e.s.Budget = time.Hour
	e.s.Now = func() time.Time { calls++; return e.now.Add(time.Duration(calls) * 25 * time.Minute) }
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	partial := e.count(t, a.Chat.ID)
	if partial == 0 || partial == 450 {
		t.Fatalf("expected partial backfill, got %d", partial)
	}

	e.s.Now = func() time.Time { return e.now }
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 450 {
		t.Fatalf("after resume = %d", n)
	}
}

func TestInterruptedSyncResumesWithoutGaps(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 450)
	e.tg.failAfter[a.Chat.ID] = 2

	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if r.OK() || r.Failed[a.Chat.ID] == nil {
		t.Fatalf("expected failure, report = %+v", r)
	}
	if n := e.count(t, a.Chat.ID); n != 200 {
		t.Fatalf("committed before failure = %d, want 200", n)
	}

	delete(e.tg.failAfter, a.Chat.ID)
	e.tg.add(a.Chat.ID, 451, 460)
	if r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a)); !r.OK() {
		t.Fatalf("second run = %+v", r)
	}
	if n := e.count(t, a.Chat.ID); n != 460 {
		t.Fatalf("after resume = %d, want 460", n)
	}
}

func TestFloodDefersChatAndContinues(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 5)
	e.tg.add(b.Chat.ID, 1, 5)
	e.tg.errs[a.Chat.ID] = tgerr.New(420, "FLOOD_WAIT_3600")

	r := e.s.Run(context.Background(), []tgclient.Target{a, b}, dialogsOf(a, b))
	if !r.OK() || !slices.Equal(r.Deferred, []int64{a.Chat.ID}) {
		t.Fatalf("report = %+v", r)
	}
	if e.count(t, b.Chat.ID) != 5 {
		t.Fatal("second chat not synced")
	}
}

func TestInaccessibleReportedOnce(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.errs[a.Chat.ID] = tgerr.New(400, "CHANNEL_PRIVATE")

	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if !slices.Equal(r.NewlyInaccessible, []int64{a.Chat.ID}) {
		t.Fatalf("first = %+v", r)
	}
	r = e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if len(r.NewlyInaccessible) != 0 {
		t.Fatalf("second = %+v", r)
	}

	delete(e.tg.errs, a.Chat.ID)
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if c, _, _ := e.st.Chat(context.Background(), a.Chat.ID); c.Status != store.ChatActive {
		t.Fatalf("status after access restored = %s", c.Status)
	}
}

func TestChatMissingFromDialogsBecomesInaccessible(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 3)
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))

	r := e.s.Run(context.Background(), nil, map[int64]bool{})
	if !slices.Equal(r.NewlyInaccessible, []int64{a.Chat.ID}) {
		t.Fatalf("report = %+v", r)
	}
	if e.count(t, a.Chat.ID) != 3 {
		t.Fatal("archived messages lost")
	}
}

func TestAuthErrorStopsRun(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.errs[a.Chat.ID] = tgerr.New(401, "AUTH_KEY_UNREGISTERED")
	e.tg.add(b.Chat.ID, 1, 5)

	r := e.s.Run(context.Background(), []tgclient.Target{a, b}, dialogsOf(a, b))
	if r.AuthErr == nil || r.OK() {
		t.Fatalf("report = %+v", r)
	}
	if e.tg.calls[b.Chat.ID] != 0 {
		t.Fatal("run continued after auth error")
	}
}

func TestForumTopicsAndPinned(t *testing.T) {
	e := newEnv(t)
	f := target(3, store.ChatForum)
	e.tg.add(f.Chat.ID, 1, 10)
	e.tg.replyTop[5] = 3
	e.tg.topics[f.Chat.ID] = []tgclient.Topic{{ID: 1, Title: "General"}, {ID: 3, Title: "Рецепты", Date: 7}}
	e.tg.pinned[f.Chat.ID] = []int{2, 9}

	if r := e.s.Run(context.Background(), []tgclient.Target{f}, dialogsOf(f)); !r.OK() {
		t.Fatalf("report = %+v", r)
	}
	ctx := context.Background()
	topics, _ := e.st.Topics(ctx, f.Chat.ID)
	if len(topics) != 2 || topics[1].Title != "Рецепты" || topics[1].CreatedAt != 7 {
		t.Fatalf("topics = %+v", topics)
	}
	pinned, _ := e.st.PinnedMessageIDs(ctx, f.Chat.ID)
	if !slices.Equal(pinned, []int{2, 9}) {
		t.Fatalf("pinned = %v", pinned)
	}
}

func TestEmptyMessagesSkippedButCursorAdvances(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 10)
	e.tg.empty[a.Chat.ID] = map[int]bool{10: true, 4: true}

	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 8 {
		t.Fatalf("messages = %d, want 8", n)
	}
	cur, _, _ := e.st.GetCursor(context.Background(), a.Chat.ID)
	if cur.NewestID != 10 {
		t.Fatalf("cursor newest = %d, want 10 (past the empty message)", cur.NewestID)
	}
}

func (e *env) has(t *testing.T, chat int64, id int) bool {
	t.Helper()
	_, err := e.st.MessageRaw(context.Background(), chat, id)
	return err == nil
}

// forumWithTopic has messages 1..10; 5 and 6 are in topic 3, the rest in General.
func forumWithTopic(e *env) tgclient.Target {
	f := target(3, store.ChatForum)
	e.tg.add(f.Chat.ID, 1, 10)
	e.tg.replyTop[5], e.tg.replyTop[6] = 3, 3
	e.tg.topics[f.Chat.ID] = []tgclient.Topic{{ID: 1, Title: "General"}, {ID: 3, Title: "Флуд"}}
	return f
}

func TestExcludedTopicIsNotArchived(t *testing.T) {
	e := newEnv(t)
	f := forumWithTopic(e)
	f.ExcludedTopics = []int{3}
	if r := e.s.Run(context.Background(), []tgclient.Target{f}, dialogsOf(f)); !r.OK() {
		t.Fatalf("report = %+v", r)
	}
	if n := e.count(t, f.Chat.ID); n != 8 || e.has(t, f.Chat.ID, 5) || e.has(t, f.Chat.ID, 6) {
		t.Fatalf("messages = %d, topic 3 kept: %v %v", n, e.has(t, f.Chat.ID, 5), e.has(t, f.Chat.ID, 6))
	}
	cur, _, _ := e.st.GetCursor(context.Background(), f.Chat.ID)
	if !cur.BackfillDone || cur.NewestID != 10 {
		t.Fatalf("cursor = %+v", cur)
	}
}

// Messages without a topic header in a forum belong to General (topic 1).
func TestExcludedGeneralTopic(t *testing.T) {
	e := newEnv(t)
	f := forumWithTopic(e)
	f.ExcludedTopics = []int{1}
	e.s.Run(context.Background(), []tgclient.Target{f}, dialogsOf(f))
	if n := e.count(t, f.Chat.ID); n != 2 || !e.has(t, f.Chat.ID, 5) {
		t.Fatalf("messages = %d, want only topic 3", n)
	}
}

func TestReincludedTopicIsBackfilled(t *testing.T) {
	e := newEnv(t)
	f := forumWithTopic(e)
	ctx := context.Background()
	f.ExcludedTopics = []int{3}
	e.s.Run(ctx, []tgclient.Target{f}, dialogsOf(f))

	f.ExcludedTopics = nil
	if r := e.s.Run(ctx, []tgclient.Target{f}, dialogsOf(f)); !r.OK() {
		t.Fatalf("report = %+v", r)
	}
	if n := e.count(t, f.Chat.ID); n != 10 {
		t.Fatalf("messages after re-include = %d, want 10", n)
	}

	before := e.tg.before
	e.s.Run(ctx, []tgclient.Target{f}, dialogsOf(f))
	if e.tg.before != before {
		t.Fatalf("an unchanged run read history backwards again (%d calls)", e.tg.before-before)
	}
}

func TestReportsProgressPerPageAndChat(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 250)
	e.tg.add(b.Chat.ID, 1, 3)
	type call struct {
		chat  int64
		saved int
		done  bool
	}
	var calls []call
	e.s.Progress = func(c store.Chat, saved int, done bool) { calls = append(calls, call{c.ID, saved, done}) }
	e.s.Run(context.Background(), []tgclient.Target{a, b}, dialogsOf(a, b))

	want := []call{
		{a.Chat.ID, 100, false}, {a.Chat.ID, 200, false}, {a.Chat.ID, 250, false}, {a.Chat.ID, 250, true},
		{b.Chat.ID, 3, false}, {b.Chat.ID, 3, true},
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("progress = %v, want %v", calls, want)
	}
}

// Every run reads the latest messages again, so an edit or a reaction on a
// recent message reaches the archive; older ones are left to other means, and
// a chat with nothing recent is not asked at all.
func TestRecentMessagesAreReadAgainForEdits(t *testing.T) {
	e := newEnv(t)
	e.now = time.Unix(1700000000, 0).Add(time.Hour)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 500)
	ctx := context.Background()
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); !r.OK() || r.NewMessages != 500 || r.Edited != 0 {
		t.Fatalf("first run = %+v", r)
	}
	e.tg.edits[a.Chat.ID] = map[int]string{480: "fixed a typo", 50: "too old to be read again"}
	e.tg.before = 0
	r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if !r.OK() || r.NewMessages != 0 || r.Edited != 1 {
		t.Fatalf("second run = %+v", r)
	}
	if e.tg.before > 3 {
		t.Errorf("read %d pages again, at most 3", e.tg.before)
	}
	text := func(id int) string {
		t.Helper()
		ms, err := e.st.PageMessages(ctx, a.Chat.ID, []int{id})
		if err != nil || len(ms) != 1 {
			t.Fatalf("message %d: %v %v", id, ms, err)
		}
		return ms[0].Text
	}
	if text(480) != "fixed a typo" {
		t.Errorf("the recent edit did not reach the archive: %q", text(480))
	}
	if text(50) != "m" {
		t.Errorf("a message outside the window was read again: %q", text(50))
	}

	// A month later nothing is recent: no request beyond the new messages.
	e.now = e.now.Add(30 * 24 * time.Hour)
	e.tg.before = 0
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if e.tg.before != 0 {
		t.Errorf("a quiet chat was read again (%d pages)", e.tg.before)
	}
}

// A message deleted in Telegram is marked, once Telegram confirms it is gone,
// and kept; one outside the recent window is left to other means.
func TestDeletedMessagesAreMarkedAndKept(t *testing.T) {
	e := newEnv(t)
	e.now = time.Unix(1700000000, 0).Add(time.Hour)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 500)
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))

	e.tg.remove(a.Chat.ID, 500, 420, 10) // the newest, one in the window, one beyond its 3 pages
	e.tg.byID = 0
	r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if !r.OK() || r.Deleted != 2 {
		t.Fatalf("report = %+v", r)
	}
	if e.count(t, a.Chat.ID) != 500 {
		t.Error("a deleted message left the archive")
	}
	deleted := func(id int) bool {
		ms, err := e.st.PageMessages(ctx, a.Chat.ID, []int{id})
		if err != nil || len(ms) != 1 {
			t.Fatalf("message %d: %v %v", id, ms, err)
		}
		return ms[0].DeletedAt != 0
	}
	if !deleted(500) || !deleted(420) || deleted(10) || deleted(421) {
		t.Errorf("marks: 500 %v, 420 %v, 10 %v, 421 %v", deleted(500), deleted(420), deleted(10), deleted(421))
	}
	// The next run asks nothing again about the ones already marked.
	e.tg.byID = 0
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Deleted != 0 || e.tg.byID != 0 {
		t.Errorf("marked again: %+v, %d lookups", r, e.tg.byID)
	}
	// One that comes back (a false alarm) loses its mark.
	e.tg.add(a.Chat.ID, 420, 420)
	slices.Sort(e.tg.ids[a.Chat.ID])
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if deleted(420) {
		t.Error("a message back in Telegram keeps its deleted mark")
	}
}

// The audit reads the whole history again a few pages a run, so an edit or a
// deletion far older than the recent window reaches the archive within a few
// runs, and the least recently audited chat goes first.
func TestAuditFindsOldEditsAndDeletions(t *testing.T) {
	e := newEnv(t) // a month after the messages: nothing is recent
	e.s.AuditPages = 3
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 500)
	e.tg.add(b.Chat.ID, 1, 50)
	ctx := context.Background()
	both := []tgclient.Target{a, b}
	e.s.AuditPages = -1
	e.s.Run(ctx, both, dialogsOf(a, b))
	e.s.AuditPages = 3

	e.tg.edits[a.Chat.ID] = map[int]string{50: "an old typo fixed"}
	e.tg.remove(a.Chat.ID, 30)
	e.tg.before = 0
	r1 := e.s.Run(ctx, both, dialogsOf(a, b))
	if e.tg.before != 3 {
		t.Errorf("first run read %d pages, the budget is 3", e.tg.before)
	}
	r2 := e.s.Run(ctx, both, dialogsOf(a, b))
	edited, deleted := r1.Edited+r2.Edited, r1.Deleted+r2.Deleted
	if edited != 1 || deleted != 1 {
		t.Fatalf("two runs found %d edited, %d deleted; reports %+v %+v", edited, deleted, r1, r2)
	}
	ms, _ := e.st.PageMessages(ctx, a.Chat.ID, []int{50, 30})
	if len(ms) != 2 || ms[0].DeletedAt == 0 || ms[1].Text != "an old typo fixed" {
		t.Errorf("messages after the audit: %+v", ms)
	}
	// Each chat is audited before any is audited twice.
	audits, _ := e.st.Audits(ctx)
	seen := map[int64]bool{}
	for _, x := range audits {
		if x.AuditedAt != 0 {
			seen[x.ChatID] = true
		}
	}
	if !seen[a.Chat.ID] && !seen[b.Chat.ID] {
		t.Fatalf("no round finished: %+v", audits)
	}
	e.s.Run(ctx, both, dialogsOf(a, b))
	audits, _ = e.st.Audits(ctx)
	for _, x := range audits {
		if x.AuditedAt == 0 {
			t.Errorf("chat %d still waits for its first round: %+v", x.ChatID, audits)
		}
	}
}
