package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

type fakeCounter map[int64]int

func (f fakeCounter) HistoryCount(_ context.Context, peer tg.InputPeerClass) (int, error) {
	p, ok := peer.(*tg.InputPeerUser)
	if !ok {
		return 0, errors.New("unexpected peer")
	}
	return f[p.UserID], nil
}

func TestPlanRowsAndPrint(t *testing.T) {
	items := []planItem{
		{ID: 1, Type: "private", Title: "Мама", Source: "folder Семья", Peer: &tg.InputPeerUser{UserID: 1}},
		{ID: 2, Type: "group", Title: "Дача", Source: "include", Peer: &tg.InputPeerUser{UserID: 2}},
	}
	rows, err := planRows(context.Background(), fakeCounter{1: 15000, 2: 12}, items)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPlan(&out, rows)
	for _, want := range []string{"would archive 2 chats, ~15 012 messages:", "Мама", "folder Семья", "~15 000", "include", "~12"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1 000", 1234567: "1 234 567"} {
		if got := groupDigits(n); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", n, got, want)
		}
	}
}
