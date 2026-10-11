package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jounderwood/tgbup/internal/store"
)

// A tick made after the checklist was downloaded reaches its card, and the
// event says which items it ticked.
func TestChecklistEventsReachThePage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const chat = int64(-1001)
	if err := db.UpsertChat(ctx, store.Chat{ID: chat, Type: store.ChatSupergroup, Title: "TODO"}); err != nil {
		t.Fatal(err)
	}
	checklist := store.Message{ChatID: chat, ID: 10, Date: 1759600000, FromID: 7, Raw: []byte{1}, TLLayer: 1,
		MetaJSON: `{"todo":{"title":"💳","items":[{"id":1,"title":"Оплатить счета"},{"id":2,"title":"Вынести мусор"}]}}`}
	tick := store.Message{ChatID: chat, ID: 11, Date: 1759610000, FromID: 7, ReplyToID: 10, Raw: []byte{1}, TLLayer: 1,
		ServiceActionJSON: `{"type":"messageActionTodoCompletions","data":{"Completed":[1],"Incompleted":null}}`}
	if err := db.SaveBatch(ctx, store.Batch{ChatID: chat, Messages: []store.Message{checklist, tick}}); err != nil {
		t.Fatal(err)
	}
	if _, err := renderArchive(ctx, db, dir, false); err != nil {
		t.Fatal(err)
	}
	pages, _ := filepath.Glob(filepath.Join(dir, "html", "TODO", "p*.html"))
	if len(pages) != 1 {
		t.Fatalf("pages = %v", pages)
	}
	b, _ := os.ReadFile(pages[0])
	page := string(b)
	for _, want := range []string{"☑ Оплатить счета", "☐ Вынести мусор", "marked done «Оплатить счета»"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "TodoCompletions") {
		t.Error("the event shows its internal name")
	}
}

// A page someone sent, downloaded before .html counted as runnable, gets
// .txt: opened next to the archive it could read every chat's search index.
func TestDownloadedPagesAreDefused(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.UpsertChat(ctx, store.Chat{ID: 10, Type: store.ChatPrivate, Title: "Mom"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBatch(ctx, store.Batch{ChatID: 10,
		Media:    []store.Media{{Key: "document:1", Kind: "document", FileName: "invoice.html"}, {Key: "document:2", Kind: "document", FileName: "a.pdf"}},
		Messages: []store.Message{{ChatID: 10, ID: 1, Date: 1, Raw: []byte{1}, MediaKeys: []string{"document:1"}}, {ChatID: 10, ID: 2, Date: 2, Raw: []byte{1}, MediaKeys: []string{"document:2"}}}}); err != nil {
		t.Fatal(err)
	}
	for key, rel := range map[string]string{"document:1": "media/2026/file/x_invoice.html", "document:2": "media/2026/file/x_a.pdf"} {
		if err := os.MkdirAll(filepath.Join(dir, "media/2026/file"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("<script>"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := db.MediaDone(ctx, key, rel); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := renderArchive(ctx, db, dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "media/2026/file/x_invoice.html.txt")); err != nil {
		t.Errorf("the page was not renamed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "media/2026/file/x_a.pdf")); err != nil {
		t.Errorf("a PDF was touched: %v", err)
	}
	paths, _ := db.DoneMediaPaths(ctx)
	for _, p := range paths {
		if p.Key == "document:1" && p.Path != "media/2026/file/x_invoice.html.txt" {
			t.Errorf("the archive still points at %s", p.Path)
		}
	}
	page, _ := os.ReadFile(filepath.Join(dir, "html/Mom/p1.html"))
	if !strings.Contains(string(page), "x_invoice.html.txt") {
		t.Error("the page does not link the renamed file")
	}
}
