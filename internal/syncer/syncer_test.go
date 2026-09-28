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

	"gitlab.com/joeunderwood/tgbp/internal/convert"
	"gitlab.com/joeunderwood/tgbp/internal/store"
	"gitlab.com/joeunderwood/tgbp/internal/syncer"
	"gitlab.com/joeunderwood/tgbp/internal/tgclient"
)

type fakeTG struct {
	mu        sync.Mutex
	ids       map[int64][]int // chat → ascending message ids
	empty     map[int64]map[int]bool
	errs      map[int64]error
	failAfter map[int64]int
	calls     map[int64]int
	before    int
	pinned    map[int64][]int
	topics    map[int64][]tgclient.Topic
	replyTop  map[int]int // message id → topic id (forum)
}

func newFake() *fakeTG {
	return &fakeTG{ids: map[int64][]int{}, empty: map[int64]map[int]bool{}, errs: map[int64]error{},
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
	p := tgclient.Page{Users: []tg.UserClass{&tg.User{ID: 5, FirstName: "Ann"}}}
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
	e := &env{st: st, tg: newFake(), now: time.Unix(1700000000, 0)}
	e.s = &syncer.Syncer{
		TG: e.tg, Store: st, SelfID: 1, Budget: time.Hour, PageSize: 100,
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
