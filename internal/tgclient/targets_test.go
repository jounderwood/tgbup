package tgclient_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"

	"github.com/joeunderwood/tgbup/internal/config"
	"github.com/joeunderwood/tgbup/internal/store"
	"github.com/joeunderwood/tgbup/internal/tgclient"
)

var dialogs = []tgclient.Dialog{
	{ID: 10, Type: store.ChatPrivate, Title: "Мама", Contact: true, TopMessage: 77},
	{ID: 11, Type: store.ChatPrivate, Title: "Незнакомец"},
	{ID: 12, Type: store.ChatPrivate, Title: "Бот", Bot: true},
	{ID: -20, Type: store.ChatGroup, Title: "Старая группа"},
	{ID: -1000000000030, Type: store.ChatForum, Title: "Семейный форум", Username: "FamForum"},
	{ID: -1000000000040, Type: store.ChatChannel, Title: "Новости"},
	{ID: -1000000000050, Type: store.ChatSupergroup, Title: "Архивная", Archived: true},
}

func chatIDs(ts []tgclient.Target) []int64 {
	var out []int64
	for _, t := range ts {
		out = append(out, t.Chat.ID)
	}
	return out
}

func TestSelectByFolderRules(t *testing.T) {
	folders := []tgclient.Folder{
		{Title: "Семья", Include: []int64{10}, Pinned: []int64{-1000000000030}},
		{Title: "Группы", Groups: true, ExcludeArchived: true, Exclude: []int64{-20}},
		{Title: "Люди", Contacts: true, NonContacts: true},
	}
	got, warns, err := tgclient.SelectTargets(dialogs, folders, config.Targets{Folders: []string{"Семья", "Группы", "Люди"}})
	if err != nil || len(warns) != 0 {
		t.Fatal(err, warns)
	}
	// -20 is excluded by "Группы", -…050 is archived, bot 12 matches no rule.
	want := []int64{-1000000000030, 10, 11}
	if !slices.Equal(chatIDs(got), want) {
		t.Fatalf("targets = %v, want %v", chatIDs(got), want)
	}
	for _, tg := range got {
		if tg.Chat.ID == -1000000000030 && !slices.Equal(tg.Chat.Folders, []string{"Семья", "Группы"}) {
			t.Errorf("forum folders = %v", tg.Chat.Folders)
		}
		if tg.Chat.ID == -1000000000030 && !tg.IsForum() {
			t.Error("forum not detected")
		}
		if tg.Chat.ID == 10 && tg.TopMessage != 77 {
			t.Errorf("top message not carried: %d", tg.TopMessage)
		}
	}
}

func TestSelectIncludeExcludeAndUnknownPeers(t *testing.T) {
	folders := []tgclient.Folder{
		// 999 and self-like peers are in the folder but not among dialogs: must be ignored, not crash.
		{Title: "Все", Include: []int64{999}, Groups: true, Broadcasts: true},
	}
	sel := config.Targets{
		Folders: []string{"Все"},
		Include: []config.PeerRef{"@famforum", "12", "@nobody"},
		Exclude: []config.PeerRef{"-1000000000040"},
	}
	got, warns, err := tgclient.SelectTargets(dialogs, folders, sel)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{-1000000000050, -1000000000030, -20, 12}
	if !slices.Equal(chatIDs(got), want) {
		t.Fatalf("targets = %v, want %v", chatIDs(got), want)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "@nobody") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestSelectUnknownFolderIsError(t *testing.T) {
	_, _, err := tgclient.SelectTargets(dialogs, nil, config.Targets{Folders: []string{"Работа"}})
	if err == nil || !strings.Contains(err.Error(), `"Работа"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestFoldersFromTelegram(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectCall(&tg.MessagesGetDialogFiltersRequest{}).ThenResult(&tg.MessagesDialogFilters{Filters: []tg.DialogFilterClass{
		&tg.DialogFilterDefault{},
		&tg.DialogFilter{
			ID: 2, Title: tg.TextWithEntities{Text: "Семья"}, Groups: true,
			IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 10}, &tg.InputPeerSelf{}},
			ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 20}},
			PinnedPeers:  []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 30}},
		},
		&tg.DialogFilterChatlist{ID: 3, Title: tg.TextWithEntities{Text: "Общая"}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 11}}},
	}})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).Folders(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("folders = %+v", got)
	}
	f := got[0]
	if f.Title != "Семья" || !f.Groups || !slices.Equal(f.Include, []int64{10, 99}) || !slices.Equal(f.Exclude, []int64{-20}) || !slices.Equal(f.Pinned, []int64{-1000000000030}) {
		t.Fatalf("folder = %+v", f)
	}
	if got[1].Title != "Общая" || !slices.Equal(got[1].Include, []int64{11}) {
		t.Fatalf("chatlist = %+v", got[1])
	}
}

func TestSelectCarriesTopicExclusions(t *testing.T) {
	sel := config.Targets{
		Include:       []config.PeerRef{"-1000000000030", "10"},
		ExcludeTopics: map[int64][]int{-1000000000030: {5, 12}, -1000000000040: {3}},
	}
	got, warns, err := tgclient.SelectTargets(dialogs, nil, sel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range got {
		want := []int(nil)
		if tt.Chat.ID == -1000000000030 {
			want = []int{5, 12}
		}
		if !slices.Equal(tt.ExcludedTopics, want) {
			t.Errorf("chat %d excluded topics = %v, want %v", tt.Chat.ID, tt.ExcludedTopics, want)
		}
	}
	// A stale exclude_topics key loses nothing, so it must not fail the run like an unresolved include.
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	notes := tgclient.TopicExclusionNotes(got, config.Targets{ExcludeTopics: map[int64][]int{
		-1000000000030: {5}, -1000000000040: {3}, 10: {1},
	}})
	if !slices.Equal(notes, []string{
		"exclude_topics -1000000000040: chat is not selected",
		"exclude_topics 10: chat is not a forum",
	}) {
		t.Fatalf("notes = %q", notes)
	}
}

func TestNameSelf(t *testing.T) {
	ds := []tgclient.Dialog{{ID: 7, Title: "Jo U"}, {ID: 8, Title: "Mom"}}
	tgclient.NameSelf(ds, 7)
	if ds[0].Title != "Saved Messages" || ds[1].Title != "Mom" {
		t.Fatalf("titles = %q, %q", ds[0].Title, ds[1].Title)
	}
}
