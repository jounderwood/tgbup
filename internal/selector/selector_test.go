package selector_test

import (
	"slices"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var (
	forum = tgclient.Dialog{ID: -1000000000001, Type: store.ChatForum, Title: "Семейный форум", Username: "FamForum"}
	mama  = tgclient.Dialog{ID: 10, Type: store.ChatPrivate, Title: "Мама", Contact: true}
	dacha = tgclient.Dialog{ID: -20, Type: store.ChatGroup, Title: "Дача"}
	old   = tgclient.Dialog{ID: 11, Type: store.ChatPrivate, Title: "Старый", Archived: true}

	dialogs = []tgclient.Dialog{forum, mama, dacha, old}
	folders = []tgclient.Folder{
		{Title: "Семья", Include: []int64{forum.ID, mama.ID}},
		{Title: "Форумы", Include: []int64{forum.ID}},
	}
)

func ids(ds []tgclient.Dialog) []int64 {
	var out []int64
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func TestGroups(t *testing.T) {
	gs := selector.Groups(dialogs, folders)
	var titles []string
	for _, g := range gs {
		titles = append(titles, g.Title)
	}
	if !slices.Equal(titles, []string{"Семья", "Форумы", selector.OutsideFolders, selector.ArchiveGroup}) {
		t.Fatalf("groups = %v", titles)
	}
	if !slices.Equal(ids(gs[0].Chats), []int64{forum.ID, mama.ID}) || !slices.Equal(ids(gs[1].Chats), []int64{forum.ID}) {
		t.Errorf("folder chats = %v / %v", ids(gs[0].Chats), ids(gs[1].Chats))
	}
	if !slices.Equal(ids(gs[2].Chats), []int64{dacha.ID}) || !slices.Equal(ids(gs[3].Chats), []int64{old.ID}) {
		t.Errorf("outside/archive = %v / %v", ids(gs[2].Chats), ids(gs[3].Chats))
	}
}

func TestSelectionIsSharedAcrossFolders(t *testing.T) {
	gs := selector.Groups(dialogs, folders)
	sel := selector.Selection{}
	sel.Toggle(gs[1].Chats, []int{0}) // forum via "Форумы"
	if sel.Count(gs[0].Chats) != 1 {
		t.Fatalf("forum selected in one folder is not seen in the other")
	}
	sel.Toggle(gs[0].Chats, []int{0}) // unselect it via "Семья"
	if sel.Count(gs[1].Chats) != 0 {
		t.Fatalf("unselecting in one folder is not reflected in the other")
	}
	sel.SetAll(gs[0].Chats, true)
	if !slices.Equal(sel.IDs(), []int64{forum.ID, mama.ID}) {
		t.Fatalf("ids = %v", sel.IDs())
	}
	sel.SetAll(gs[0].Chats, false)
	if len(sel.IDs()) != 0 {
		t.Fatalf("ids after clear = %v", sel.IDs())
	}
}

func TestParseIndices(t *testing.T) {
	got, err := selector.ParseIndices(" 1, 3 ,5-7,3 ", 8)
	if err != nil || !slices.Equal(got, []int{0, 2, 4, 5, 6}) {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"0", "3-1", "1-", "9", "abc", "", " , "} {
		if _, err := selector.ParseIndices(bad, 8); err == nil {
			t.Errorf("ParseIndices(%q) accepted", bad)
		}
	}
}

func TestSearch(t *testing.T) {
	if got := ids(selector.Search(dialogs, "МАМ")); !slices.Equal(got, []int64{mama.ID}) {
		t.Errorf("title search = %v", got)
	}
	if got := ids(selector.Search(dialogs, "famforum")); !slices.Equal(got, []int64{forum.ID}) {
		t.Errorf("username search = %v", got)
	}
}

func TestInitialFromConfig(t *testing.T) {
	sel, warns := selector.Initial(dialogs, folders, config.Targets{
		Folders: []string{"Семья"}, Include: []config.PeerRef{"-20"}, Exclude: []config.PeerRef{"10"},
	})
	if !slices.Equal(sel.IDs(), []int64{forum.ID, dacha.ID}) || len(warns) != 0 {
		t.Fatalf("initial = %v %v", sel.IDs(), warns)
	}

	sel, warns = selector.Initial(dialogs, folders, config.Targets{Folders: []string{"Удалённая"}})
	if len(sel) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "Удалённая") {
		t.Fatalf("missing folder: %v %v", sel.IDs(), warns)
	}
}
