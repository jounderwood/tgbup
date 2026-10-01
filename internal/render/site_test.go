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
		"html/Family/index.html", "html/Family/General/2026-02.html", "html/Family/Expenses/2026-03.html",
		"html/Family/before upgrade/2025-12.html", "html/Mom/2026-03.html", "html/search/-1001.js",
		"md/index.md", "md/Family/Expenses/2026-03.md", "md/Mom/2026-03.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	expenses := read(t, filepath.Join(dir, "html/Family/Expenses/2026-03.html"))
	if !strings.Contains(expenses, `href="../General/2026-02.html#m10"`) {
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
	for _, want := range []string{"Expenses", `href="Expenses/2026-03.html"`, "before upgrade", `href="before%20upgrade/2025-12.html"`} {
		if !strings.Contains(chat, want) {
			t.Errorf("chat index lacks %q:\n%s", want, chat)
		}
	}
	if s := read(t, filepath.Join(dir, "html/search/-1001.js")); !strings.Contains(s, "rent paid") || !strings.Contains(s, "Family/Expenses/2026-03.html#m11") {
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
	// A directory where a page file should go makes that one write fail.
	if err := os.MkdirAll(filepath.Join(dir, "html/Mom/2026-03.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Render(context.Background(), db, Options{OutDir: dir, Track: true, Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Pages != 3 {
		t.Fatalf("result = %+v", res)
	}
	// The failed page is retried next time rather than marked as done.
	os.Remove(filepath.Join(dir, "html/Mom/2026-03.html"))
	if res, _ := Render(context.Background(), db, Options{OutDir: dir, Track: true, Loc: time.UTC}); res.Pages != 1 {
		t.Fatalf("retry drew %d pages", res.Pages)
	}
}

// A page's place and neighbours are part of what it shows: a renamed chat
// moves its pages, and a new month adds a "next" link to the previous one.
func TestRenderRedrawsMovedPagesAndNewNeighbours(t *testing.T) {
	db, dir := siteFixture(t)
	ctx := context.Background()
	opts := Options{OutDir: dir, Track: true, Loc: time.UTC}
	if _, err := Render(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat(ctx, store.Chat{ID: 10, Type: store.ChatPrivate, Title: "Mother"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBatch(ctx, store.Batch{ChatID: -1001, Messages: []store.Message{
		{ChatID: -1001, ID: 13, TopicID: 160, Date: at("2026-04-01", "10:00"), Text: "april", Raw: []byte{1}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "html/Mother/2026-03.html")); err != nil {
		t.Errorf("renamed chat's page not written to its new folder: %v", err)
	}
	if s := read(t, filepath.Join(dir, "html/search/10.js")); !strings.Contains(s, "Mother/2026-03.html") {
		t.Errorf("search still points at the old folder: %s", s)
	}
	if s := read(t, filepath.Join(dir, "html/Family/Expenses/2026-03.html")); !strings.Contains(s, `href="2026-04.html"`) {
		t.Error("March page has no link to the new April page")
	}
}
