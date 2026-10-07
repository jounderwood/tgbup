package stats_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/joeunderwood/tgbup/internal/stats"
	"github.com/joeunderwood/tgbup/internal/store"
	"github.com/joeunderwood/tgbup/internal/tgclient"
)

type fake struct {
	total  map[int64]int
	types  map[int64]tgclient.TypeCounts
	topics map[int64][]tgclient.Topic
	perTop map[int]int
	fail   map[int64]error
	asked  []int // topic ids whose count was requested
}

func id(p tg.InputPeerClass) int64 { return p.(*tg.InputPeerUser).UserID }

func (f *fake) HistoryCount(_ context.Context, p tg.InputPeerClass) (int, error) {
	if err := f.fail[id(p)]; err != nil {
		return 0, err
	}
	return f.total[id(p)], nil
}

func (f *fake) SearchCounters(_ context.Context, p tg.InputPeerClass) (tgclient.TypeCounts, error) {
	return f.types[id(p)], nil
}

func (f *fake) ForumTopics(_ context.Context, p tg.InputPeerClass) ([]tgclient.Topic, error) {
	return f.topics[id(p)], nil
}

func (f *fake) TopicMessageCount(_ context.Context, _ tg.InputPeerClass, topic int) (int, error) {
	f.asked = append(f.asked, topic)
	return f.perTop[topic], nil
}

func item(n int64, typ string, excluded ...int) stats.Item {
	return stats.Item{ID: n, Type: typ, Title: "c", Peer: &tg.InputPeerUser{UserID: n}, ExcludedTopics: excluded}
}

func TestCollect(t *testing.T) {
	f := &fake{
		total:  map[int64]int{1: 4812, 2: 15300},
		types:  map[int64]tgclient.TypeCounts{2: {Photos: 1204, Voice: 630}},
		topics: map[int64][]tgclient.Topic{1: {{ID: 1, Title: "General"}, {ID: 3, Title: "Recipes"}, {ID: 5, Title: "Flood"}}},
		perTop: map[int]int{3: 300, 5: 2900},
		fail:   map[int64]error{3: errors.New("FLOOD_WAIT_600")},
	}
	var progress []int
	got := stats.Collect(context.Background(), f,
		[]stats.Item{item(1, store.ChatForum, 5, 99), item(3, store.ChatGroup), item(2, store.ChatPrivate)},
		func(done, total int) { progress = append(progress, done*10+total) })

	forum := got[0]
	if forum.Err != nil || forum.Total != 4812 || forum.Saved() != 4812-2900 {
		t.Fatalf("forum = %+v, saved %d", forum, forum.Saved())
	}
	wantTopics := []stats.TopicStats{{ID: 1, Title: "General", Count: 4812 - 300 - 2900}, {ID: 3, Title: "Recipes", Count: 300}, {ID: 5, Title: "Flood", Count: 2900, Excluded: true}}
	if !slices.Equal(forum.Topics, wantTopics) {
		t.Fatalf("topics = %+v", forum.Topics)
	}
	if !slices.Equal(forum.MissingTopics, []int{99}) {
		t.Errorf("missing topics = %v, want [99]", forum.MissingTopics)
	}
	if slices.Contains(f.asked, 1) {
		t.Error("General's count must be derived, not requested")
	}
	if got[1].Err == nil || got[1].Item.ID != 3 {
		t.Fatalf("failed chat = %+v", got[1])
	}
	if got[2].Err != nil || got[2].Types.Voice != 630 || got[2].Saved() != 15300 {
		t.Fatalf("private chat = %+v", got[2])
	}
	if !slices.Equal(progress, []int{13, 23, 33}) {
		t.Fatalf("progress = %v", progress)
	}
}

func TestCollectStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := stats.Collect(ctx, &fake{}, []stats.Item{item(1, store.ChatPrivate)}, nil)
	if !errors.Is(got[0].Err, context.Canceled) {
		t.Fatalf("err = %v", got[0].Err)
	}
}
