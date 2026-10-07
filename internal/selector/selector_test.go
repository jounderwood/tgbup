package selector_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/joeunderwood/tgbup/internal/config"
	"github.com/joeunderwood/tgbup/internal/selector"
	"github.com/joeunderwood/tgbup/internal/store"
	"github.com/joeunderwood/tgbup/internal/tgclient"
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
	if !slices.Equal(titles, []string{selector.AllChats, "Семья", "Форумы", selector.OutsideFolders, selector.ArchiveGroup}) {
		t.Fatalf("groups = %v", titles)
	}
	if !slices.Equal(ids(gs[0].Chats), ids(dialogs)) {
		t.Errorf("all chats = %v", ids(gs[0].Chats))
	}
	if !slices.Equal(ids(gs[1].Chats), []int64{forum.ID, mama.ID}) || !slices.Equal(ids(gs[2].Chats), []int64{forum.ID}) {
		t.Errorf("folder chats = %v / %v", ids(gs[1].Chats), ids(gs[2].Chats))
	}
	if !slices.Equal(ids(gs[3].Chats), []int64{dacha.ID}) || !slices.Equal(ids(gs[4].Chats), []int64{old.ID}) {
		t.Errorf("outside/archive = %v / %v", ids(gs[3].Chats), ids(gs[4].Chats))
	}
}

func TestSelectionIsSharedAcrossFolders(t *testing.T) {
	gs := selector.Groups(dialogs, folders)[1:]
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
	for _, q := range []string{"famforum", "@FamForum"} {
		if got := ids(selector.Search(dialogs, q)); !slices.Equal(got, []int64{forum.ID}) {
			t.Errorf("username search %q = %v", q, got)
		}
	}
}

func TestInitialFromConfig(t *testing.T) {
	sel, warns := selector.Initial(dialogs, folders, config.Targets{
		Folders: []string{"Семья"}, Include: []config.PeerRef{"-20"}, Exclude: []config.PeerRef{"10"},
	})
	if !slices.Equal(sel.Chats.IDs(), []int64{forum.ID, dacha.ID}) || len(warns) != 0 {
		t.Fatalf("initial = %v %v", sel.Chats.IDs(), warns)
	}

	sel, warns = selector.Initial(dialogs, folders, config.Targets{Folders: []string{"Удалённая"}})
	if len(sel.Chats) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "Удалённая") {
		t.Fatalf("missing folder: %v %v", sel.Chats.IDs(), warns)
	}
}

func TestInitialKeepsTheRestWhenAFolderIsGone(t *testing.T) {
	sel, warns := selector.Initial(dialogs, folders, config.Targets{
		Folders: []string{"Семья", "Удалённая"}, Include: []config.PeerRef{"-20"},
	})
	if !slices.Equal(sel.Chats.IDs(), []int64{forum.ID, dacha.ID, mama.ID}) {
		t.Fatalf("initial = %v", sel.Chats.IDs())
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "Удалённая") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestInitialTopicExclusions(t *testing.T) {
	st, _ := selector.Initial(dialogs, folders, config.Targets{
		Include:       []config.PeerRef{"-1000000000001"},
		ExcludeTopics: map[int64][]int{forum.ID: {5, 12}, dacha.ID: {3}},
	})
	if !slices.Equal(st.ExcludedFor(forum.ID), []int{5, 12}) || st.ExcludedFor(dacha.ID) != nil {
		t.Fatalf("excluded = %v / %v", st.ExcludedFor(forum.ID), st.ExcludedFor(dacha.ID))
	}
	if st.TopicArchived(forum.ID, 5) || !st.TopicArchived(forum.ID, 1) {
		t.Fatal("topic marks do not follow the config")
	}
}

func TestTopicToggles(t *testing.T) {
	all := []int{1, 3, 5}
	st := selector.NewState()

	// Marking a topic of an unselected forum selects the forum with only that topic.
	st.ToggleTopics(forum.ID, all, []int{1})
	if !st.Chats[forum.ID] || !slices.Equal(st.ExcludedFor(forum.ID), []int{1, 5}) {
		t.Fatalf("after marking topic 3: selected %v, excluded %v", st.Chats[forum.ID], st.ExcludedFor(forum.ID))
	}
	st.SetAllTopics(forum.ID, all, true)
	if !st.Chats[forum.ID] || st.ExcludedFor(forum.ID) != nil {
		t.Fatalf("all topics on: excluded %v", st.ExcludedFor(forum.ID))
	}
	// Unmarking every topic leaves nothing to archive, so the forum is unselected.
	st.ToggleTopics(forum.ID, all, []int{0, 1, 2})
	if st.Chats[forum.ID] || st.ExcludedFor(forum.ID) != nil {
		t.Fatalf("no topics left: selected %v, excluded %v", st.Chats[forum.ID], st.ExcludedFor(forum.ID))
	}
	st.ToggleTopics(forum.ID, all, []int{2})
	st.SetAllTopics(forum.ID, all, false)
	if st.Chats[forum.ID] {
		t.Fatal("n on the topic screen kept the forum selected")
	}
}

// A forum whose topic list came back empty must not be dropped by "all on".
func TestTopicTogglesWithoutTopicsKeepForum(t *testing.T) {
	st := selector.NewState()
	st.Chats[forum.ID] = true
	st.SetAllTopics(forum.ID, nil, true)
	st.ToggleTopics(forum.ID, nil, nil)
	if !st.Chats[forum.ID] {
		t.Fatal("empty topic list deselected the forum")
	}
}
