package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeunderwood/tgbup/internal/store"
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
		MetaJSON: `{"todo":{"title":"💳","items":[{"id":1,"title":"Оплатить кассу"},{"id":2,"title":"Аренда"}]}}`}
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
	for _, want := range []string{"☑ Оплатить кассу", "☐ Аренда", "marked done «Оплатить кассу»"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "TodoCompletions") {
		t.Error("the event shows its internal name")
	}
}
