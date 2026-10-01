package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

func siteFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	chats := []store.Chat{
		{ID: -1001, Type: store.ChatForum, Title: "Family", MigratedFromID: -45},
		{ID: -45, Type: store.ChatGroup, Title: "Family"},
		{ID: 10, Type: store.ChatPrivate, Title: "../Mom/"},
	}
	for _, c := range chats {
		if err := db.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertTopics(ctx, []store.Topic{{ChatID: -1001, TopicID: 1, Title: "General"}, {ChatID: -1001, TopicID: 160, Title: "Expenses"}}); err != nil {
		t.Fatal(err)
	}
	save := func(chat int64, ms ...store.Message) {
		t.Helper()
		for i := range ms {
			ms[i].ChatID, ms[i].Raw, ms[i].FromID = chat, []byte{1}, 5
		}
		if err := db.SaveBatch(ctx, store.Batch{ChatID: chat, Users: []store.User{{ID: 5, FirstName: "Ann"}},
			Media: []store.Media{{Key: "photo:7", Kind: "photo"}}, Messages: ms}); err != nil {
			t.Fatal(err)
		}
	}
	save(-1001,
		store.Message{ID: 10, TopicID: 1, Date: at("2026-02-10", "10:00"), Text: "february hello"},
		store.Message{ID: 11, TopicID: 160, Date: at("2026-03-01", "10:00"), Text: "rent paid", ReplyToID: 10},
		store.Message{ID: 12, TopicID: 160, Date: at("2026-03-02", "10:00"), MediaKeys: []string{"photo:7"}},
	)
	save(-45, store.Message{ID: 3, Date: at("2025-12-01", "10:00"), Text: "before the upgrade"})
	save(10, store.Message{ID: 1, Date: at("2026-03-05", "08:00"), Text: "hi mom"})
	return db, dir
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRenderSite(t *testing.T) {
	db, dir := siteFixture(t)
	ctx := context.Background()
	opts := Options{OutDir: dir, Track: true, Loc: time.UTC}
	res, err := Render(ctx, db, opts)
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("render = %+v %v", res, err)
	}
	if res.Pages != 4 {
		t.Fatalf("pages = %d, want 4", res.Pages)
	}
	for _, p := range []string{
		"html/index.html", "html/assets/style.css", "html/assets/search.js",
		"html/Family/index.html", "html/Family/General/p1.html", "html/Family/Expenses/p1.html",
		"html/Family/Expenses/index.html", "html/Family/Expenses/dates.html",
		"html/Family/before upgrade/p1.html", "html/Mom/p1.html", "html/Mom/index.html", "html/search/-1001.js",
		"md/index.md", "md/Family/Expenses/p1.md", "md/Family/Expenses/dates.md", "md/Mom/p1.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	expenses := read(t, filepath.Join(dir, "html/Family/Expenses/p1.html"))
	if !strings.Contains(expenses, `href="../General/p1.html#m10"`) {
		t.Errorf("reply across topics not linked:\n%s", expenses)
	}
	index := read(t, filepath.Join(dir, "html/index.html"))
	for _, want := range []string{`href="Family/index.html"`, `href="Mom/index.html"`, `"search/-1001.js"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index lacks %q", want)
		}
	}
	// Search indexes can be hundreds of MB for big chats: they load on first search, not with the chat list.
	if strings.Contains(index, `<script src="search/`) {
		t.Error("index.html loads search indexes eagerly")
	}
	chat := read(t, filepath.Join(dir, "html/Family/index.html"))
	for _, want := range []string{"Expenses", `href="Expenses/p1.html"`, "before upgrade", `href="before%20upgrade/p1.html"`, `href="Expenses/dates.html"`} {
		if !strings.Contains(chat, want) {
			t.Errorf("chat index lacks %q:\n%s", want, chat)
		}
	}
	if s := read(t, filepath.Join(dir, "html/search/-1001.js")); !strings.Contains(s, "rent paid") || !strings.Contains(s, "Family/Expenses/p1.html#m11") {
		t.Errorf("search index = %s", s)
	}

	// Nothing changed: no month page is drawn again.
	if res, _ := Render(ctx, db, opts); res.Pages != 0 {
		t.Fatalf("unchanged archive redrew %d pages", res.Pages)
	}
	// A file finishing its download changes exactly its own month.
	if err := db.MediaDone(ctx, "photo:7", "media/photo/7.jpg"); err != nil {
		t.Fatal(err)
	}
	if res, _ := Render(ctx, db, opts); res.Pages != 1 {
		t.Fatalf("a downloaded photo redrew %d pages, want 1", res.Pages)
	}
	if res, _ := Render(ctx, db, Options{OutDir: dir, Track: true, Full: true, Loc: time.UTC}); res.Pages != 4 {
		t.Fatalf("--full drew %d pages", res.Pages)
	}
}

func TestRenderKeepsGoingAfterAPageFails(t *testing.T) {
	db, dir := siteFixture(t)
	if _, err := Render(context.Background(), db, Options{OutDir: dir, Track: true, Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	// A directory where a page file should go makes that one write fail.
	page := filepath.Join(dir, "html/Mom/p1.html")
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(page, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Render(context.Background(), db, Options{OutDir: dir, Track: true, Full: true, Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Pages != 3 {
		t.Fatalf("result = %+v", res)
	}
	// The failed page is retried next time rather than marked as done.
	os.Remove(filepath.Join(dir, "html/Mom/p1.html"))
	if res, _ := Render(context.Background(), db, Options{OutDir: dir, Track: true, Loc: time.UTC}); res.Pages != 1 {
		t.Fatalf("retry drew %d pages", res.Pages)
	}
}

// A page's place is part of what it shows: a renamed chat moves its pages.
func TestRenderRedrawsMovedPages(t *testing.T) {
	db, dir := siteFixture(t)
	ctx := context.Background()
	opts := Options{OutDir: dir, Track: true, Loc: time.UTC}
	if _, err := Render(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat(ctx, store.Chat{ID: 10, Type: store.ChatPrivate, Title: "Mother"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "html/Mother/p1.html")); err != nil {
		t.Errorf("renamed chat's page not written to its new folder: %v", err)
	}
	if s := read(t, filepath.Join(dir, "html/search/10.js")); !strings.Contains(s, "Mother/p1.html") {
		t.Errorf("search still points at the old folder: %s", s)
	}
}

// Pages hold a fixed number of messages counted from the oldest: new messages
// touch only the last page, until a new page appears and every pager grows.
func TestRenderPaging(t *testing.T) {
	db, dir := siteFixture(t)
	ctx := context.Background()
	opts := Options{OutDir: dir, Track: true, Loc: time.UTC, PageSize: 2}
	add := func(id int, day string) {
		t.Helper()
		if err := db.SaveBatch(ctx, store.Batch{ChatID: 10, Messages: []store.Message{
			{ChatID: 10, ID: id, Date: at(day, "09:00"), Text: "msg " + day, Raw: []byte{1}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	add(2, "2026-03-06") // Mom: 2 messages → one full page
	if _, err := Render(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	add(3, "2026-03-07")
	res, _ := Render(ctx, db, opts)
	// A new page 2: page 1's pager changes too.
	if res.Pages != 2 {
		t.Fatalf("a message that opens a new page redrew %d pages, want 2", res.Pages)
	}
	add(4, "2026-03-08")
	if res, _ := Render(ctx, db, opts); res.Pages != 1 {
		t.Fatalf("a message on the last page redrew %d pages, want 1", res.Pages)
	}
	if s := read(t, filepath.Join(dir, "html/Mom/index.html")); !strings.Contains(s, "p2.html") {
		t.Errorf("chat does not open on the latest page: %s", s)
	}
	dates := read(t, filepath.Join(dir, "html/Mom/dates.html"))
	for _, want := range []string{"March 2026", `href="p1.html#d2026-03-05"`, `href="p2.html#d2026-03-08"`, "page 2"} {
		if !strings.Contains(dates, want) {
			t.Errorf("calendar lacks %q:\n%s", want, dates)
		}
	}
}

// Month pages from the previous layout are removed when the archive is redrawn as pages.
func TestRenderClearsOldLayout(t *testing.T) {
	db, dir := siteFixture(t)
	old := filepath.Join(dir, "html/Mom/2026-03.html")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(context.Background(), db, Options{OutDir: dir, Track: true, Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an old month page was left behind")
	}
}

func TestRenderMediaIndex(t *testing.T) {
	db, dir := siteFixture(t)
	ctx := context.Background()
	if err := db.SaveBatch(ctx, store.Batch{ChatID: 10, Messages: []store.Message{
		{ChatID: 10, ID: 2, Date: at("2026-03-06", "09:00"), Text: "see example.com and docs", Raw: []byte{1},
			EntitiesJSON: `[{"type":"messageEntityUrl","offset":4,"length":11},{"type":"messageEntityTextUrl","offset":20,"length":4,"url":"https://docs.example.com/x"}]`},
		{ChatID: 10, ID: 3, Date: at("2026-03-06", "10:00"), Raw: []byte{1},
			MetaJSON: `{"geo":{"lat":44.81,"long":20.46},"venue":{"title":"Kafana","address":"Knez Mihailova 1"}}`},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.MediaDone(ctx, "photo:7", "media/photo/7.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, db, Options{OutDir: dir, Track: true, Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	mom := read(t, filepath.Join(dir, "html/media/10.js"))
	for _, want := range []string{`"k":"link"`, `"f":"https://example.com"`, `"f":"https://docs.example.com/x"`,
		`"k":"place"`, `"n":"Kafana, Knez Mihailova 1"`, `"m":"Mom/p1.html#m3"`} {
		if !strings.Contains(mom, want) {
			t.Errorf("Mom media index lacks %s:\n%s", want, mom)
		}
	}
	family := read(t, filepath.Join(dir, "html/media/-1001.js"))
	for _, want := range []string{`"k":"photo"`, `"f":"../media/photo/7.jpg"`, `"t":"Expenses"`, `"m":"Family/Expenses/p1.html#m12"`} {
		if !strings.Contains(family, want) {
			t.Errorf("Family media index lacks %s:\n%s", want, family)
		}
	}
	page := read(t, filepath.Join(dir, "html/media.html"))
	for _, want := range []string{`"media/-1001.js"`, `"media/10.js"`, "Family", `id="types"`} {
		if !strings.Contains(page, want) {
			t.Errorf("media.html lacks %q", want)
		}
	}
	if !strings.Contains(read(t, filepath.Join(dir, "html/index.html")), `href="media.html"`) {
		t.Error("archive index does not link the media page")
	}
	if !strings.Contains(read(t, filepath.Join(dir, "html/Family/index.html")), `href="../media.html#chat=-1001"`) {
		t.Error("chat index does not link its media")
	}
}

// A new tgbup with changed templates redraws pages whose messages did not change.
func TestTemplateChangeRedraws(t *testing.T) {
	db, dir := siteFixture(t)
	ctx := context.Background()
	opts := Options{OutDir: dir, Track: true, Loc: time.UTC}
	if _, err := Render(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	saved := templatesHash
	templatesHash = "changed"
	defer func() { templatesHash = saved }()
	if res, _ := Render(ctx, db, opts); res.Pages != 4 {
		t.Fatalf("changed templates redrew %d pages, want 4", res.Pages)
	}
}

func TestPageLinksItsTopicMedia(t *testing.T) {
	db, dir := siteFixture(t)
	if _, err := Render(context.Background(), db, Options{OutDir: dir, Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	if s := read(t, filepath.Join(dir, "html/Family/Expenses/p1.html")); !strings.Contains(s, `href="../../media.html#chat=-1001&topic=Expenses"`) {
		t.Errorf("topic page does not link its media:\n%s", s)
	}
	if s := read(t, filepath.Join(dir, "html/Mom/p1.html")); !strings.Contains(s, `href="../media.html#chat=10"`) {
		t.Errorf("chat page does not link its media")
	}
	if !strings.Contains(read(t, filepath.Join(dir, "html/media.html")), `id="topic"`) {
		t.Error("media page has no topic filter")
	}
}
