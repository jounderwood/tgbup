package tgclient_test

import (
	"context"
	"slices"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"

	"gitlab.com/joeunderwood/tgbp/internal/tgclient"
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
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(48, 49, 50)})

	page, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryBefore(context.Background(), peer, 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page); !slices.Equal(got, []int{49, 48}) {
		t.Fatalf("ids = %v", got)
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
