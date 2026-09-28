package tgclient_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"

	"gitlab.com/joeunderwood/tgbp/internal/config"
	"gitlab.com/joeunderwood/tgbp/internal/store"
	"gitlab.com/joeunderwood/tgbp/internal/tgclient"
)

var dialogs = []tgclient.Dialog{
	{ID: 10, Type: store.ChatPrivate, Title: "Мама", Contact: true},
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
	got, err := tgclient.NewAPI(tg.NewClient(mock)).Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("folders = %+v", got)
	}
	f := got[0]
	if f.Title != "Семья" || !f.Groups || !slices.Equal(f.Include, []int64{10}) || !slices.Equal(f.Exclude, []int64{-20}) || !slices.Equal(f.Pinned, []int64{-1000000000030}) {
		t.Fatalf("folder = %+v", f)
	}
	if got[1].Title != "Общая" || !slices.Equal(got[1].Include, []int64{11}) {
		t.Fatalf("chatlist = %+v", got[1])
	}
}
