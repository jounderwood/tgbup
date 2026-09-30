package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/stats"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var (
	sForum = tgclient.Dialog{ID: -1000000000001, Type: store.ChatForum, Title: "Family forum"}
	sMama  = tgclient.Dialog{ID: 10, Type: store.ChatPrivate, Title: "Мама"}
	sDacha = tgclient.Dialog{ID: -20, Type: store.ChatGroup, Title: "Дача"}
	sOld   = tgclient.Dialog{ID: 11, Type: store.ChatPrivate, Title: "Старый", Archived: true}
)

type fakeSetup struct {
	asked    []int64 // chat ids whose row stats were requested
	noTopics bool
}

func (f *fakeSetup) rowStats(ds []tgclient.Dialog) map[int64]rowInfo {
	out := map[int64]rowInfo{}
	for _, d := range ds {
		f.asked = append(f.asked, d.ID)
		switch {
		case d.ID == sDacha.ID:
			out[d.ID] = rowInfo{Err: errors.New("CHANNEL_PRIVATE")}
		case d.Type == store.ChatForum:
			out[d.ID] = rowInfo{Msgs: 4812, Topics: 2}
		default:
			out[d.ID] = rowInfo{Msgs: 5}
		}
	}
	return out
}

func (f *fakeSetup) details(items []stats.Item) []stats.ChatStats {
	var out []stats.ChatStats
	for _, it := range items {
		cs := stats.ChatStats{Item: it, Total: 5}
		if it.Type == store.ChatForum && !f.noTopics {
			cs.Total = 30
			for _, tp := range []stats.TopicStats{{ID: 1, Title: "General", Count: 10}, {ID: 5, Title: "Flood", Count: 20}} {
				tp.Excluded = slices.Contains(it.ExcludedTopics, tp.ID)
				cs.Topics = append(cs.Topics, tp)
			}
		}
		out = append(out, cs)
	}
	return out
}

func newDialogOver(input string, st *selector.State, dialogs []tgclient.Dialog, folders []tgclient.Folder) (*setupDialog, *bytes.Buffer, *fakeSetup) {
	var out bytes.Buffer
	f := &fakeSetup{}
	return &setupDialog{
		ctx: context.Background(), in: readLines(strings.NewReader(input), nil), out: &out,
		groups: selector.Groups(dialogs, folders), dialogs: dialogs, st: st, cfgPath: "/c.yaml",
		rowStats: f.rowStats, details: f.details,
	}, &out, f
}

func newDialog(input string, st *selector.State) (*setupDialog, *bytes.Buffer) {
	d, out, _ := newDialogOver(input, st,
		[]tgclient.Dialog{sForum, sMama, sDacha, sOld},
		[]tgclient.Folder{{Title: "Family", Include: []int64{sForum.ID, sMama.ID}}})
	return d, out
}

func state(ids ...int64) *selector.State {
	st := selector.NewState()
	for _, id := range ids {
		st.Chats[id] = true
	}
	return st
}

func wantOutput(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestSetupSelectsAcrossGroupsAndSaves(t *testing.T) {
	// Family → 1 (forum) → back; No folder → 1 (Дача) → back; done; yes.
	d, out := newDialog("2\n1\n\n3\n1\n\nd\ny\n", state())
	if !d.run() {
		t.Fatalf("not saved:\n%s", out)
	}
	if got := d.st.Chats.IDs(); !slices.Equal(got, []int64{sForum.ID, sDacha.ID}) {
		t.Fatalf("ids = %v", got)
	}
	wantOutput(t, out.String(), "1) All chats", "Family", "(2, selected 0)", "[x]  Family forum", "would archive 2 chats",
		"Write to /c.yaml (2 chats)?")
}

func TestSetupListShowsCountsAndTopics(t *testing.T) {
	d, out := newDialog("1\n\nq\n", state())
	d.run()
	wantOutput(t, out.String(), "All chats · page 1/1 · 4 chats · 0 selected",
		"forum    ~4 812    2", "private  ~5", "group    ?")
}

func TestSetupStartsFromCurrentSelection(t *testing.T) {
	d, out := newDialog("q\n", state(sMama.ID))
	if d.run() {
		t.Fatal("q must not save")
	}
	wantOutput(t, out.String(), "Selected: 1 chats", "(2, selected 1)")
}

func TestSetupDeclineAndSearch(t *testing.T) {
	d, out := newDialog("s\nмам\n1\n\nd\nN\nq\n", state())
	if d.run() {
		t.Fatal("declined confirmation saved")
	}
	if !slices.Equal(d.st.Chats.IDs(), []int64{sMama.ID}) {
		t.Fatalf("search selection = %v", d.st.Chats.IDs())
	}
	wantOutput(t, out.String(), "Search: мам", "not written")
}

func TestSetupAcceptsYes(t *testing.T) {
	for _, ans := range []string{"y", "yes", "YES", "да"} {
		d, _ := newDialog("2\n1\n\nd\n"+ans+"\n", state())
		if !d.run() {
			t.Errorf("%q did not save", ans)
		}
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	d, out := newDialog("2\n9\n0\nt 2\n\nx\nd\nq\n", state())
	if d.run() {
		t.Fatal("saved")
	}
	if len(d.st.Chats) != 0 {
		t.Fatalf("bad input changed selection: %v", d.st.Chats.IDs())
	}
	wantOutput(t, out.String(), "error: invalid number", "not a forum", `unknown command "x"`, "nothing selected")
}

func TestSetupPaging(t *testing.T) {
	var many []tgclient.Dialog
	for i := range 45 {
		many = append(many, tgclient.Dialog{ID: int64(1000 + i), Type: store.ChatPrivate, Title: fmt.Sprintf("Chat %02d", i+1)})
	}
	input := "1\n>\n21-23\n>\n45\n<\np 99\np 1\na\n<\nN\n\nq\n"
	d, out, f := newDialogOver(input, state(), many, nil)
	d.run()
	wantOutput(t, out.String(),
		"All chats · page 1/3 · 45 chats", "page 2/3", "  21  [ ]  Chat 21", "  21  [x]  Chat 21",
		"page 3/3", "  45  [x]  Chat 45", "error: no page 99 (1-3)", "· 24 selected", "already on the first page")
	if len(d.st.Chats) != 0 {
		t.Fatalf("N left %d chats selected", len(d.st.Chats))
	}
	seen := map[int64]bool{}
	for _, id := range f.asked {
		if seen[id] {
			t.Fatalf("row stats of %d fetched twice", id)
		}
		seen[id] = true
	}
}

func TestSetupTopicsScreen(t *testing.T) {
	// All chats → topics of the forum → unmark Flood → back → back → done → yes.
	d, out := newDialog("1\nt 1\n2\n\n\nd\ny\n", state(sForum.ID))
	if !d.run() {
		t.Fatalf("not saved:\n%s", out)
	}
	if got := d.st.ExcludedFor(sForum.ID); !slices.Equal(got, []int{5}) {
		t.Fatalf("excluded = %v", got)
	}
	wantOutput(t, out.String(), "Family forum · topics · new topics are archived automatically",
		"[x]   General #1", "[ ]   Flood #5", "excluded:       Flood #5 ~20", "2 (1 excl)")
}

func TestSetupEOFExitsWithoutSaving(t *testing.T) {
	for _, input := range []string{"", "2\n1", "d\n", "2\n1\n\nd\n", "1\nt 1\n"} {
		d, _ := newDialog(input, state(sForum.ID))
		if d.run() {
			t.Errorf("input %q: EOF saved the config", input)
		}
	}
}

// Ctrl+C cancels the context while the dialog waits for a line that never comes.
func TestSetupInterruptExitsWithoutSaving(t *testing.T) {
	d, _ := newDialog("", state())
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx, d.in = ctx, make(chan string)
	done := make(chan bool)
	go func() { done <- d.run() }()
	cancel()
	select {
	case saved := <-done:
		if saved {
			t.Fatal("interrupted dialog saved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dialog ignores interrupt")
	}
}

func TestReadLinesReportsReadErrors(t *testing.T) {
	var got error
	lines := readLines(strings.NewReader(strings.Repeat("x", 70*1024)+"\n"), func(err error) { got = err })
	for range lines {
	}
	if got == nil {
		t.Fatal("a line over the scanner limit ended input silently")
	}
}

func TestSetupTopicsScreenWithoutTopics(t *testing.T) {
	d, out, f := newDialogOver("1\nt 1\na\n\nq\n", state(sForum.ID), []tgclient.Dialog{sForum}, nil)
	f.noTopics = true
	d.run()
	if !d.st.Chats[sForum.ID] {
		t.Fatal("forum with no loaded topics got deselected")
	}
	wantOutput(t, out.String(), "no topics to choose from")
}
