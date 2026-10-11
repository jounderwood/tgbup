package syncer_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"

	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

func (e *env) message(t *testing.T, chat int64, id int) store.RenderMessage {
	t.Helper()
	ms, err := e.st.PageMessages(context.Background(), chat, []int{id})
	if err != nil || len(ms) != 1 {
		t.Fatalf("message %d/%d: %v %v", chat, id, ms, err)
	}
	return ms[0]
}

func (e *env) versions(t *testing.T, chat int64, id int) []string {
	t.Helper()
	vs, err := e.st.MessageVersions(context.Background(), chat, []int{id})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range vs[id] {
		out = append(out, v.Text)
	}
	return out
}

// feedAt is where the archive follows a feed from; 0: not followed.
func (e *env) feedAt(t *testing.T, box int64) int {
	t.Helper()
	f, _, err := e.st.Feed(context.Background(), box)
	if err != nil {
		t.Fatal(err)
	}
	return f.Pts
}

// A channel's feed brings an edit, a reaction and a deletion however old the
// message, with neither the recent window nor the audit reading it again.
func TestFeedBringsOldEditsAndDeletions(t *testing.T) {
	e := newEnv(t) // a month after the messages: nothing is recent; no audit
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 500)
	ctx := context.Background()
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); !r.OK() || r.NewMessages != 500 {
		t.Fatalf("first run = %+v", r)
	}
	if got := e.feedAt(t, a.Chat.ID); got != feedBase {
		t.Fatalf("the channel's feed is followed from %d, want where its history said: %d", got, feedBase)
	}

	e.tg.editLater(a.Chat.ID, 10, "an old typo fixed")
	e.tg.deleteLater(a.Chat.ID, 20, 21)
	e.tg.reactLater(a.Chat.ID, 30, "👍")
	e.tg.before, e.tg.byID = 0, 0
	r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if !r.OK() || r.Edited != 1 || r.Updated != 1 || r.Deleted != 2 || r.NewMessages != 0 {
		t.Fatalf("second run = %+v", r)
	}
	if e.tg.before != 0 {
		t.Errorf("history read again (%d pages): the feed alone should do", e.tg.before)
	}
	if m := e.message(t, a.Chat.ID, 10); m.Text != "an old typo fixed" {
		t.Errorf("message 10 = %q", m.Text)
	}
	if vs := e.versions(t, a.Chat.ID, 10); !slices.Equal(vs, []string{"m"}) {
		t.Errorf("versions before the edit = %q", vs)
	}
	if m := e.message(t, a.Chat.ID, 30); !strings.Contains(m.ReactionsJSON, "👍") {
		t.Errorf("the reaction did not reach the archive: %q", m.ReactionsJSON)
	}
	for _, id := range []int{20, 21} {
		if e.message(t, a.Chat.ID, id).DeletedAt == 0 {
			t.Errorf("message %d is not marked deleted", id)
		}
	}
	if e.count(t, a.Chat.ID) != 500 {
		t.Error("a deleted message left the archive")
	}
	if got := e.feedAt(t, a.Chat.ID); got != e.tg.ptsOf(a.Chat.ID) {
		t.Errorf("feed at %d, want %d", got, e.tg.ptsOf(a.Chat.ID))
	}

	// Nothing new: one request, nothing read again.
	e.tg.byID = 0
	reads := len(e.tg.feedReads[a.Chat.ID])
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Edited+r.Deleted+r.Updated != 0 || e.tg.byID != 0 {
		t.Errorf("a quiet feed changed something: %+v, %d lookups", r, e.tg.byID)
	}
	if n := len(e.tg.feedReads[a.Chat.ID]) - reads; n != 1 {
		t.Errorf("a quiet feed took %d requests", n)
	}
}

// The account's own feed covers private chats and basic groups: an edit says
// its chat; a deletion gives only ids, which name messages there and never
// the channel messages with the same numbers. It may bring a channel's edit
// too, which goes to the channel.
func TestOwnFeedFindsTheChatsOfItsChanges(t *testing.T) {
	e := newEnv(t)
	p, g, ch := privateTarget(42), groupTarget(77), target(1, store.ChatSupergroup)
	// The account numbers the messages of private chats and basic groups in
	// one sequence; a channel has its own.
	e.tg.add(p.Chat.ID, 1, 50)
	e.tg.add(g.Chat.ID, 51, 80)
	e.tg.add(ch.Chat.ID, 1, 80)
	all := []tgclient.Target{p, g, ch}
	ctx := context.Background()
	if r := e.s.Run(ctx, all, dialogsOf(all...)); !r.OK() {
		t.Fatalf("first run = %+v", r)
	}
	if got := e.feedAt(t, 0); got != feedBase {
		t.Fatalf("own feed at %d", got)
	}

	e.tg.editLater(p.Chat.ID, 5, "fixed")
	e.tg.deleteLater(g.Chat.ID, 60)
	e.tg.deleteLater(p.Chat.ID, 7)
	e.tg.feeds[0] = append(e.tg.feeds[0], feedEvent{deleted: []int{999}}) // in a chat not archived
	// The channel's 60 and 7 are gone too, unannounced: a deletion in the
	// account's feed must not reach them.
	e.tg.remove(ch.Chat.ID, 60, 7)
	e.tg.editSilently(ch.Chat.ID, 3, "fixed in the channel")
	e.tg.feeds[0] = append(e.tg.feeds[0], feedEvent{edited: e.tg.message(ch.Chat.ID, 3), chat: ch.Chat.ID})
	r := e.s.Run(ctx, all, dialogsOf(all...))
	if !r.OK() || r.Edited != 2 || r.Deleted != 2 {
		t.Fatalf("second run = %+v", r)
	}
	if e.message(t, p.Chat.ID, 5).Text != "fixed" {
		t.Error("the edit in the private chat did not reach the archive")
	}
	if e.message(t, ch.Chat.ID, 3).Text != "fixed in the channel" {
		t.Error("the channel's edit in the account's feed did not reach the channel")
	}
	if e.message(t, g.Chat.ID, 60).DeletedAt == 0 || e.message(t, p.Chat.ID, 7).DeletedAt == 0 {
		t.Error("deletions in the group and the private chat are not marked")
	}
	if e.message(t, ch.Chat.ID, 60).DeletedAt != 0 || e.message(t, ch.Chat.ID, 7).DeletedAt != 0 {
		t.Error("a channel message with the same number was marked deleted")
	}
	if got := e.feedAt(t, 0); got != e.tg.ptsOf(0) {
		t.Errorf("own feed at %d, want %d", got, e.tg.ptsOf(0))
	}
}

// The feed only brings archived messages up to date: one the archive does
// not have yet is the forward sync's or the backfill's, and so is one in an
// excluded topic.
func TestFeedAddsNoMessages(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatForum)
	a.ExcludedTopics = []int{7}
	e.tg.add(a.Chat.ID, 1, 50)
	e.tg.topics[a.Chat.ID] = []tgclient.Topic{{ID: 7, Title: "off"}}
	e.tg.replyTop[40] = 7
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 49 {
		t.Fatalf("archived %d, want 49 (one in the excluded topic)", n)
	}

	e.tg.add(a.Chat.ID, 51, 51)
	e.tg.editLater(a.Chat.ID, 51, "new, then edited")
	e.tg.editLater(a.Chat.ID, 40, "edited in the excluded topic")
	e.tg.brokenAfter = true // the forward sync brings nothing this run
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Edited != 0 || e.count(t, a.Chat.ID) != 49 {
		t.Fatalf("the feed added a message: %+v, %d archived", r, e.count(t, a.Chat.ID))
	}
	e.tg.brokenAfter = false
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.NewMessages != 1 || e.message(t, a.Chat.ID, 51).Text != "new, then edited" {
		t.Fatalf("the forward sync did not bring it as it is now: %+v", r)
	}
}

// A version the feed gives never takes the archive back: an edit older than
// the version the audit already brought is left, and an edit of a message
// marked deleted is checked with Telegram before the mark goes.
func TestFeedNeverTakesTheArchiveBack(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 50)
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))

	e.tg.editLater(a.Chat.ID, 10, "v1")
	e.tg.editLater(a.Chat.ID, 10, "v2")
	e.tg.editLater(a.Chat.ID, 12, "edited, then deleted")
	e.tg.remove(a.Chat.ID, 12) // unannounced
	// A run that reads no feed (its share spent) but audits the chat.
	e.s.FeedRequests, e.s.AuditPages = -1, 10
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Edited != 1 || r.Deleted != 1 {
		t.Fatalf("audit run = %+v", r)
	}
	e.s.FeedRequests, e.s.AuditPages = 0, -1
	r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if r.Edited != 0 || r.Updated != 0 || r.Deleted != 0 {
		t.Errorf("the feed changed the archive back: %+v", r)
	}
	if m := e.message(t, a.Chat.ID, 10); m.Text != "v2" {
		t.Errorf("message 10 = %q, want the latest", m.Text)
	}
	if vs := e.versions(t, a.Chat.ID, 10); !slices.Equal(vs, []string{"m"}) {
		t.Errorf("versions = %q, want only the one before v2", vs)
	}
	if e.message(t, a.Chat.ID, 12).DeletedAt == 0 {
		t.Error("an edit from before the deletion took the deleted mark away")
	}
}

// A change that is no edit (reactions) keeps the edit time: the feed's copy
// may be older than what the archive has, so Telegram is asked again.
func TestFeedRereadsOnTheSameEditTime(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 50)
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))

	e.tg.reactions[a.Chat.ID] = map[int]string{10: "👍"}
	older := e.tg.message(a.Chat.ID, 10)
	e.tg.reactions[a.Chat.ID][10] = "🔥"
	e.s.FeedRequests, e.s.AuditPages = -1, 10 // the audit brings 🔥 first
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Updated != 1 {
		t.Fatalf("audit run = %+v", r)
	}
	e.tg.feeds[a.Chat.ID] = append(e.tg.feeds[a.Chat.ID], feedEvent{edited: older, chat: a.Chat.ID})
	e.s.FeedRequests, e.s.AuditPages = 0, -1
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Updated != 0 {
		t.Errorf("the feed's older copy changed the archive: %+v", r)
	}
	if m := e.message(t, a.Chat.ID, 10); !strings.Contains(m.ReactionsJSON, "🔥") {
		t.Errorf("reactions = %q, want the latest", m.ReactionsJSON)
	}
}

// A long feed is read a page at a time within the run's share of requests,
// and goes on in the next run from the last page that reached the archive.
func TestFeedPagesAndGoesOnNextRun(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 50)
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	for id := 1; id <= 5; id++ {
		e.tg.editLater(a.Chat.ID, id, "fixed")
	}
	e.tg.feedPage, e.s.FeedRequests = 2, 2
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Edited != 4 {
		t.Fatalf("first run edited %d, want 4 (2 pages of 2)", r.Edited)
	}
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Edited != 1 {
		t.Fatalf("second run edited %d, want the 1 left", r.Edited)
	}
	reads := e.tg.feedReads[a.Chat.ID]
	if !slices.Equal(reads[len(reads)-3:], []int{feedBase, feedBase + 2, feedBase + 4}) {
		t.Errorf("reads started from %v", reads)
	}

	// Out of time: no feed is read, and none moves.
	e.tg.editLater(a.Chat.ID, 6, "fixed")
	e.s.Budget = 0
	before := len(e.tg.feedReads[a.Chat.ID])
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if len(e.tg.feedReads[a.Chat.ID]) != before || e.feedAt(t, a.Chat.ID) != feedBase+5 {
		t.Error("a feed was read with no time left")
	}
}

// Feeds take turns: when a run's share of requests runs out, the next run
// starts with the ones left.
func TestFeedsTakeTurns(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 10)
	e.tg.add(b.Chat.ID, 1, 10)
	both := []tgclient.Target{a, b}
	ctx := context.Background()
	e.s.Run(ctx, both, dialogsOf(a, b))
	e.s.FeedRequests = 1
	for range 4 {
		e.now = e.now.Add(time.Hour)
		e.s.Run(ctx, both, dialogsOf(a, b))
	}
	if na, nb := len(e.tg.feedReads[a.Chat.ID]), len(e.tg.feedReads[b.Chat.ID]); na != 2 || nb != 2 {
		t.Errorf("reads: %d and %d, want 2 each", na, nb)
	}
}

// When Telegram lost track of a feed (too long since the last run), it goes
// on from now; one whose place Telegram does not say starts again from the
// chat's history in the same run.
func TestFeedLostTrackGoesOnFromNow(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	p := privateTarget(42)
	e.tg.add(a.Chat.ID, 1, 50)
	e.tg.add(b.Chat.ID, 1, 50)
	e.tg.add(p.Chat.ID, 1, 50)
	all := []tgclient.Target{a, b, p}
	ctx := context.Background()
	e.s.Run(ctx, all, dialogsOf(all...))

	e.tg.feeds[a.Chat.ID] = append(e.tg.feeds[a.Chat.ID], feedEvent{}, feedEvent{})
	e.tg.feeds[b.Chat.ID] = append(e.tg.feeds[b.Chat.ID], feedEvent{}, feedEvent{}, feedEvent{})
	e.tg.tooLong[a.Chat.ID] = true
	e.tg.tooLong[b.Chat.ID], e.tg.lost[b.Chat.ID] = true, true
	// The account's own feed lost track, but goes on from the new place.
	e.tg.feeds[0] = append(e.tg.feeds[0], feedEvent{}, feedEvent{})
	e.tg.tooLong[0] = true
	e.tg.editLater(p.Chat.ID, 5, "in the gap")
	r := e.s.Run(ctx, all, dialogsOf(all...))
	if !r.OK() || r.Edited != 0 {
		t.Fatalf("report = %+v: a change in the lost gap is the window's and the audit's", r)
	}
	if got := e.feedAt(t, a.Chat.ID); got != e.tg.ptsOf(a.Chat.ID) {
		t.Errorf("feed a at %d, want now: %d", got, e.tg.ptsOf(a.Chat.ID))
	}
	if got := e.feedAt(t, b.Chat.ID); got != e.tg.ptsOf(b.Chat.ID) {
		t.Errorf("feed b at %d, want started again from its history: %d", got, e.tg.ptsOf(b.Chat.ID))
	}
	if got := e.feedAt(t, 0); got != e.tg.ptsOf(0) {
		t.Errorf("own feed at %d, want %d", got, e.tg.ptsOf(0))
	}
	// From the new place on, changes come as before.
	e.tg.editLater(p.Chat.ID, 6, "after the gap")
	e.tg.editLater(a.Chat.ID, 7, "after the gap")
	if r := e.s.Run(ctx, all, dialogsOf(all...)); r.Edited != 2 {
		t.Errorf("changes after the new place: %+v", r)
	}
}

// A feed Telegram fails on keeps its place for the next run and the others
// go on; one Telegram refuses to go on with from its place goes on from now.
// A run never fails for a feed.
func TestFeedFailureKeepsItsPlace(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 50)
	e.tg.add(b.Chat.ID, 1, 50)
	both := []tgclient.Target{a, b}
	ctx := context.Background()
	e.s.Run(ctx, both, dialogsOf(a, b))
	e.tg.editLater(a.Chat.ID, 3, "fixed")
	e.tg.editLater(b.Chat.ID, 4, "fixed")
	e.tg.feedErr[a.Chat.ID] = tgerr.New(500, "INTERNAL")
	r := e.s.Run(ctx, both, dialogsOf(a, b))
	if !r.OK() || r.Edited != 1 {
		t.Fatalf("report = %+v", r)
	}
	if got := e.feedAt(t, a.Chat.ID); got != feedBase {
		t.Errorf("the failed feed moved to %d", got)
	}
	delete(e.tg.feedErr, a.Chat.ID)
	if r := e.s.Run(ctx, both, dialogsOf(a, b)); r.Edited != 1 || e.message(t, a.Chat.ID, 3).Text != "fixed" {
		t.Errorf("the next run did not bring the edit: %+v", r)
	}

	// A flood wait stops all feeds for the run, each keeping its place.
	e.tg.editLater(a.Chat.ID, 5, "x")
	e.tg.editLater(b.Chat.ID, 6, "y")
	atA, atB := e.feedAt(t, a.Chat.ID), e.feedAt(t, b.Chat.ID)
	e.tg.feedErr[a.Chat.ID] = tgerr.New(420, "FLOOD_WAIT_600")
	e.tg.feedErr[b.Chat.ID] = tgerr.New(420, "FLOOD_WAIT_600")
	if r := e.s.Run(ctx, both, dialogsOf(a, b)); !r.OK() {
		t.Errorf("a flood wait in the feeds failed the run: %+v", r)
	}
	if len(e.tg.feedReads[a.Chat.ID])+len(e.tg.feedReads[b.Chat.ID]) != 5 {
		t.Errorf("the feeds went on after a flood wait: %v", e.tg.feedReads)
	}
	if e.feedAt(t, a.Chat.ID) != atA || e.feedAt(t, b.Chat.ID) != atB {
		t.Error("a feed moved in a flood wait")
	}

	// Telegram refusing the account (a frozen one) stops all feeds the same.
	e.tg.feedErr[a.Chat.ID] = tgerr.New(420, "FROZEN_METHOD_INVALID")
	e.tg.feedErr[b.Chat.ID] = tgerr.New(420, "FROZEN_METHOD_INVALID")
	before := len(e.tg.feedReads[a.Chat.ID]) + len(e.tg.feedReads[b.Chat.ID])
	e.s.Run(ctx, both, dialogsOf(a, b))
	if n := len(e.tg.feedReads[a.Chat.ID]) + len(e.tg.feedReads[b.Chat.ID]) - before; n != 1 {
		t.Errorf("%d feeds read after the account was refused, want 1", n)
	}
	if e.feedAt(t, a.Chat.ID) != atA || e.feedAt(t, b.Chat.ID) != atB {
		t.Error("a feed moved when the account was refused")
	}

	// Refused from where it stood (a place Telegram no longer knows): it
	// goes on from now instead of failing every run.
	delete(e.tg.feedErr, b.Chat.ID)
	e.tg.feedErr[a.Chat.ID] = tgerr.New(400, "PERSISTENT_TIMESTAMP_INVALID")
	e.s.Run(ctx, both, dialogsOf(a, b))
	if got := e.feedAt(t, a.Chat.ID); got != e.tg.ptsOf(a.Chat.ID) {
		t.Errorf("a refused feed at %d, want now: %d", got, e.tg.ptsOf(a.Chat.ID))
	}
}

// Reading changed messages again by id: a failure that may pass leaves the
// page for the next run; Telegram refusing the chat skips it.
func TestFeedRereadFailure(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 50)
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	e.tg.deleteLater(a.Chat.ID, 20)
	e.tg.byIDErr[a.Chat.ID] = tgerr.New(500, "INTERNAL")
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); !r.OK() || r.Deleted != 0 {
		t.Fatalf("report = %+v", r)
	}
	if got := e.feedAt(t, a.Chat.ID); got != feedBase {
		t.Fatalf("the page moved on without its deletion: feed at %d", got)
	}
	delete(e.tg.byIDErr, a.Chat.ID)
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Deleted != 1 {
		t.Fatalf("the next run did not mark it: %+v", r)
	}

	// Not for this chat's sake (a frozen account): the page waits too.
	e.tg.deleteLater(a.Chat.ID, 21)
	at := e.feedAt(t, a.Chat.ID)
	e.tg.byIDErr[a.Chat.ID] = tgerr.New(420, "FROZEN_METHOD_INVALID")
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if got := e.feedAt(t, a.Chat.ID); got != at {
		t.Errorf("the page moved on without its deletion: feed at %d, was %d", got, at)
	}

	// Telegram does not take the ids: the chat is skipped, the feed goes on.
	e.tg.byIDErr[a.Chat.ID] = tgerr.New(400, "MSG_ID_INVALID")
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	if got := e.feedAt(t, a.Chat.ID); got != e.tg.ptsOf(a.Chat.ID) {
		t.Errorf("a refused chat held the feed at %d", got)
	}
}

// The same failure feed after feed is the account's or the connection's: the
// feeds stop after three, each keeping its place.
func TestFeedsStopAfterThreeFailures(t *testing.T) {
	e := newEnv(t)
	var all []tgclient.Target
	for n := int64(1); n <= 5; n++ {
		x := target(n, store.ChatSupergroup)
		e.tg.add(x.Chat.ID, 1, 10)
		all = append(all, x)
	}
	ctx := context.Background()
	e.s.Run(ctx, all, dialogsOf(all...))
	for _, x := range all {
		e.tg.feedErr[x.Chat.ID] = tgerr.New(403, "SOMETHING_NEW")
	}
	r := e.s.Run(ctx, all, dialogsOf(all...))
	if !r.OK() {
		t.Fatalf("report = %+v", r)
	}
	reads := 0
	for _, x := range all {
		reads += len(e.tg.feedReads[x.Chat.ID])
		if e.feedAt(t, x.Chat.ID) != feedBase {
			t.Errorf("feed %d moved", x.Chat.ID)
		}
	}
	if reads != 3 {
		t.Errorf("%d feeds read, want 3", reads)
	}
}

// A deletion the archive has marked already is not asked about again: a page
// read again after a stop goes on where it stopped.
func TestFeedSkipsDeletionsMarkedAlready(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 50)
	ctx := context.Background()
	e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a))
	e.tg.deleteLater(a.Chat.ID, 25)
	e.s.FeedRequests, e.s.AuditPages = -1, 10 // the audit finds it first
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Deleted != 1 {
		t.Fatalf("audit run = %+v", r)
	}
	e.s.FeedRequests, e.s.AuditPages = 0, -1
	e.tg.byID = 0
	if r := e.s.Run(ctx, []tgclient.Target{a}, dialogsOf(a)); r.Deleted != 0 || e.tg.byID != 0 {
		t.Errorf("asked again about a deletion marked already: %+v, %d lookups", r, e.tg.byID)
	}
}

// Telegram answers ids of private chats and basic groups from any of them
// (an archive kept on after a login with another account): a message it
// gives for another chat is neither saved into this one nor taken for gone.
func TestRereadIgnoresAnotherChatsMessage(t *testing.T) {
	e := newEnv(t)
	p, g := privateTarget(42), groupTarget(77)
	e.tg.add(p.Chat.ID, 1, 10)
	e.tg.add(g.Chat.ID, 11, 20)
	both := []tgclient.Target{p, g}
	ctx := context.Background()
	e.s.Run(ctx, both, dialogsOf(p, g))
	e.tg.remove(p.Chat.ID, 5)
	e.tg.add(g.Chat.ID, 5, 5)
	e.tg.feeds[0] = append(e.tg.feeds[0], feedEvent{deleted: []int{5}})
	if r := e.s.Run(ctx, both, dialogsOf(p, g)); r.Deleted != 0 || r.Updated+r.Edited != 0 {
		t.Errorf("report = %+v", r)
	}
	if m := e.message(t, p.Chat.ID, 5); m.DeletedAt != 0 {
		t.Error("marked deleted for another chat's message")
	}
}

// Without the feeds (none given, or none to read), where a channel's stands
// is still noted, so the feeds can start later.
func TestFeedsOffStillNoteWhereChannelsStand(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 10)
	e.s.Feeds = nil
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if got := e.feedAt(t, a.Chat.ID); got != feedBase {
		t.Errorf("feed at %d", got)
	}
	if len(e.tg.feedReads[a.Chat.ID]) != 0 {
		t.Error("a feed was read with no feeds given")
	}
}
