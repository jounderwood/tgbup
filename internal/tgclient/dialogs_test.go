package tgclient

import (
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbp/internal/store"
)

func TestMergeDialogsDeduplicatesArchived(t *testing.T) {
	all := []Dialog{{ID: 1, Title: "a"}, {ID: 2, Title: "archived"}}
	archived := []Dialog{{ID: 2, Title: "archived", Archived: true}, {ID: 3, Title: "archive-only", Archived: true}}

	got := mergeDialogs(all, archived)
	if len(got) != 3 {
		t.Fatalf("dialogs = %+v", got)
	}
	want := map[int64]bool{1: false, 2: true, 3: true}
	for _, d := range got {
		if d.Archived != want[d.ID] {
			t.Errorf("dialog %d archived = %v", d.ID, d.Archived)
		}
	}
}

func TestDeletedAccountDialogIsKept(t *testing.T) {
	users := map[int64]*tg.User{7: {ID: 7, AccessHash: 1, Deleted: true}}
	e := dialogs.Elem{
		Dialog:   &tg.Dialog{Peer: &tg.PeerUser{UserID: 7}, TopMessage: 42},
		Peer:     &tg.InputPeerUser{UserID: 7, AccessHash: 1},
		Entities: peer.NewEntities(users, map[int64]*tg.Chat{}, map[int64]*tg.Channel{}),
	}
	d, ok := dialogFromElem(e, false)
	if !ok {
		t.Fatal("deleted account dropped: its history is still readable")
	}
	if d.ID != 7 || d.Type != store.ChatPrivate || d.Title != "Deleted Account" || d.TopMessage != 42 {
		t.Fatalf("dialog = %+v", d)
	}
}
