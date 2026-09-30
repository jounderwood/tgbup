package main

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var (
	sForum = tgclient.Dialog{ID: -1000000000001, Type: store.ChatForum, Title: "Семейный форум"}
	sMama  = tgclient.Dialog{ID: 10, Type: store.ChatPrivate, Title: "Мама"}
	sDacha = tgclient.Dialog{ID: -20, Type: store.ChatGroup, Title: "Дача"}
	sOld   = tgclient.Dialog{ID: 11, Type: store.ChatPrivate, Title: "Старый", Archived: true}
)

func newDialog(input string, initial selector.Selection) (*setupDialog, *bytes.Buffer) {
	dialogs := []tgclient.Dialog{sForum, sMama, sDacha, sOld}
	folders := []tgclient.Folder{{Title: "Семья", Include: []int64{sForum.ID, sMama.ID}}}
	var out bytes.Buffer
	groups := selector.Groups(dialogs, folders)
	return &setupDialog{
		in: bufio.NewScanner(strings.NewReader(input)), out: &out,
		groups: groups, dialogs: dialogs, sel: initial, cfgPath: "/c.yaml",
		plan: func(chosen []tgclient.Dialog) ([]planRow, error) {
			var rows []planRow
			for _, it := range dialogItems(chosen, groups) {
				rows = append(rows, planRow{ID: it.ID, Type: it.Type, Title: it.Title, Source: it.Source, Count: 5})
			}
			return rows, nil
		},
	}, &out
}

func TestSetupSelectsAcrossGroupsAndSaves(t *testing.T) {
	// Семья → 1 (форум) → назад; Вне папок → 1 (Дача) → назад; готово; да.
	d, out := newDialog("1\n1\n\n2\n1\n\nd\ny\n", selector.Selection{})
	if !d.run() {
		t.Fatalf("not saved:\n%s", out)
	}
	if got := d.sel.IDs(); !slices.Equal(got, []int64{sForum.ID, sDacha.ID}) {
		t.Fatalf("ids = %v", got)
	}
	for _, want := range []string{"Семья (2, выбрано 0)", "[x] Семейный форум", "would archive 2 chats", "Записать в /c.yaml (2 чатов)?"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSetupStartsFromCurrentSelection(t *testing.T) {
	d, out := newDialog("q\n", selector.Selection{sMama.ID: true})
	if d.run() {
		t.Fatal("q must not save")
	}
	if !strings.Contains(out.String(), "Выбрано: 1 чатов") || !strings.Contains(out.String(), "Семья (2, выбрано 1)") {
		t.Fatalf("initial selection not shown:\n%s", out)
	}
}

func TestSetupDeclineAndSearch(t *testing.T) {
	// поиск «мам» → отметить → назад; готово; нет; выйти.
	d, out := newDialog("s\nмам\n1\n\nd\nN\nq\n", selector.Selection{})
	if d.run() {
		t.Fatal("declined confirmation saved")
	}
	if !slices.Equal(d.sel.IDs(), []int64{sMama.ID}) || !strings.Contains(out.String(), "Поиск: мам") {
		t.Fatalf("search selection = %v\n%s", d.sel.IDs(), out)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	d, out := newDialog("1\n9\n0\n\nx\nd\nq\n", selector.Selection{})
	if d.run() {
		t.Fatal("saved")
	}
	if len(d.sel) != 0 {
		t.Fatalf("bad input changed selection: %v", d.sel.IDs())
	}
	for _, want := range []string{"ошибка:", "неизвестная команда \"x\"", "ничего не выбрано"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSetupEOFExitsWithoutSaving(t *testing.T) {
	for _, input := range []string{"", "1\n1", "d\n", "1\n1\n\nd\n"} {
		d, _ := newDialog(input, selector.Selection{})
		if d.run() {
			t.Errorf("input %q: EOF saved the config", input)
		}
	}
}
