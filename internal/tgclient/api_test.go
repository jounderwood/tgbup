package tgclient_test

import (
	"context"
	"slices"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"

	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var peer = &tg.InputPeerChannel{ChannelID: 1, AccessHash: 2}

func msgs(ids ...int) []tg.MessageClass {
	out := make([]tg.MessageClass, len(ids))
	for i, id := range ids {
		out[i] = &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 1}}
	}
	return out
}

func ids(p tgclient.Page) []int {
	var out []int
	for _, m := range p.Messages {
		out = append(out, m.GetID())
	}
	return out
}

func TestHistoryAfterRequestsWindowAboveCursor(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		req, ok := b.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("request %T", b)
		}
		if req.OffsetID != 11 || req.AddOffset != -3 || req.Limit != 3 || req.MinID != 10 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(13, 12, 11, 10)})

	page, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryAfter(context.Background(), peer, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page); !slices.Equal(got, []int{11, 12, 13}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestHistoryBeforeFiltersAndSortsDesc(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		req := b.(*tg.MessagesGetHistoryRequest)
		if req.OffsetID != 50 || req.Limit != 100 || req.AddOffset != 0 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{
		Messages: msgs(48, 49, 50),
		Chats:    []tg.ChatClass{&tg.Channel{ID: 8, Title: "Новости", Photo: &tg.ChatPhotoEmpty{}}},
	})

	page, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryBefore(context.Background(), peer, 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page); !slices.Equal(got, []int{49, 48}) {
		t.Fatalf("ids = %v", got)
	}
	if len(page.Chats) != 1 {
		t.Fatalf("chats vector dropped: %+v", page.Chats)
	}
}

func TestPinnedIDsPages(t *testing.T) {
	mock := tgmock.NewRequire(t)
	first := make([]int, 100)
	for i := range first {
		first[i] = 300 - i
	}
	mock.ExpectFunc(func(b bin.Encoder) {
		req := b.(*tg.MessagesSearchRequest)
		if _, ok := req.Filter.(*tg.InputMessagesFilterPinned); !ok || req.OffsetID != 0 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(first...)})
	mock.ExpectFunc(func(b bin.Encoder) {
		if req := b.(*tg.MessagesSearchRequest); req.OffsetID != 201 {
			t.Fatalf("second offset = %d", req.OffsetID)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(5)})

	got, err := tgclient.NewAPI(tg.NewClient(mock)).PinnedIDs(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 101 || got[100] != 5 {
		t.Fatalf("pinned = %d items, last %d", len(got), got[len(got)-1])
	}
}

// peer and from_id are required TL fields: a nil value fails encoding in the mock.
func topic(id int, title string, closed bool, date int) *tg.ForumTopic {
	return &tg.ForumTopic{
		ID: id, Title: title, Closed: closed, Date: date,
		Peer: &tg.PeerChannel{ChannelID: 1}, FromID: &tg.PeerUser{UserID: 1},
	}
}

func TestForumTopics(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		if _, ok := b.(*tg.MessagesGetForumTopicsRequest); !ok {
			t.Fatalf("request %T", b)
		}
	}).ThenResult(&tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
		topic(1, "General", false, 10),
		topic(7, "Рецепты", true, 20),
		&tg.ForumTopicDeleted{ID: 9},
	}})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).ForumTopics(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != (tgclient.Topic{ID: 7, Title: "Рецепты", Closed: true, Date: 20}) {
		t.Fatalf("topics = %+v", got)
	}
}

func TestFileChunk(t *testing.T) {
	mock := tgmock.NewRequire(t)
	loc := &tg.InputDocumentFileLocation{ID: 5}
	mock.ExpectFunc(func(b bin.Encoder) {
		req := b.(*tg.UploadGetFileRequest)
		if req.Offset != 524288 || req.Limit != 524288 || req.CDNSupported {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.UploadFile{Type: &tg.StorageFilePartial{}, Bytes: []byte("abc")})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).FileChunk(context.Background(), loc, 524288, 524288)
	if err != nil || string(got) != "abc" {
		t.Fatalf("chunk = %q %v", got, err)
	}
}

func TestHistoryCount(t *testing.T) {
	cases := []struct {
		name string
		res  tg.MessagesMessagesClass
		want int
	}{
		{"slice", &tg.MessagesMessagesSlice{Count: 4812, Messages: msgs(9)}, 4812},
		{"channel", &tg.MessagesChannelMessages{Count: 220, Messages: msgs(9)}, 220},
		{"full", &tg.MessagesMessages{Messages: msgs(3, 2, 1)}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock := tgmock.NewRequire(t)
			mock.ExpectFunc(func(b bin.Encoder) {
				if req := b.(*tg.MessagesGetHistoryRequest); req.Limit != 1 {
					t.Fatalf("limit = %d, want 1", req.Limit)
				}
			}).ThenResult(c.res)
			got, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryCount(context.Background(), peer)
			if err != nil || got != c.want {
				t.Fatalf("count = %d %v, want %d", got, err, c.want)
			}
		})
	}
}

func TestSearchCounters(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		if req := b.(*tg.MessagesGetSearchCountersRequest); len(req.Filters) != 8 {
			t.Fatalf("filters = %d, want 8", len(req.Filters))
		}
	}).ThenResult(&tg.MessagesSearchCounterVector{Elems: []tg.MessagesSearchCounter{
		{Filter: &tg.InputMessagesFilterURL{}, Count: 55},
		{Filter: &tg.InputMessagesFilterPhotos{}, Count: 310},
		{Filter: &tg.InputMessagesFilterVideo{}, Count: 42},
		{Filter: &tg.InputMessagesFilterDocument{}, Count: 18},
		{Filter: &tg.InputMessagesFilterVoice{}, Count: 97},
		{Filter: &tg.InputMessagesFilterRoundVideo{}, Count: 12},
		{Filter: &tg.InputMessagesFilterMusic{}, Count: 3},
		{Filter: &tg.InputMessagesFilterGif{}, Count: 4},
	}})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).SearchCounters(context.Background(), peer)
	want := tgclient.TypeCounts{Photos: 310, Videos: 42, Files: 18, Voice: 97, Round: 12, Music: 3, GIFs: 4, Links: 55}
	if err != nil || got != want {
		t.Fatalf("counts = %+v %v, want %+v", got, err, want)
	}
}

func TestTopicMessageCount(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		if req := b.(*tg.MessagesGetRepliesRequest); req.MsgID != 5 || req.Limit != 1 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Count: 2900, Messages: msgs(9)})
	if n, err := tgclient.NewAPI(tg.NewClient(mock)).TopicMessageCount(context.Background(), peer, 5); err != nil || n != 2900 {
		t.Fatalf("count = %d %v", n, err)
	}
}
