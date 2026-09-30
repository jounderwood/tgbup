package tgclient

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gotd/td/tg"
)

type Page struct {
	Messages []tg.MessageClass
	Users    []tg.UserClass
	Chats    []tg.ChatClass
}

type Topic struct {
	ID     int
	Title  string
	Closed bool
	Date   int
}

type Telegram interface {
	HistoryBefore(ctx context.Context, peer tg.InputPeerClass, beforeID, limit int) (Page, error)
	HistoryAfter(ctx context.Context, peer tg.InputPeerClass, afterID, limit int) (Page, error)
	PinnedIDs(ctx context.Context, peer tg.InputPeerClass) ([]int, error)
	ForumTopics(ctx context.Context, peer tg.InputPeerClass) ([]Topic, error)
	Message(ctx context.Context, peer tg.InputPeerClass, id int) (tg.MessageClass, error)
	FileChunk(ctx context.Context, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error)
}

type API struct {
	raw *tg.Client
}

func NewAPI(raw *tg.Client) *API { return &API{raw: raw} }

func (a *API) HistoryBefore(ctx context.Context, peer tg.InputPeerClass, beforeID, limit int) (Page, error) {
	res, err := a.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: beforeID, Limit: limit})
	if err != nil {
		return Page{}, err
	}
	p, err := toPage(res)
	if err != nil {
		return Page{}, err
	}
	if beforeID > 0 {
		p.Messages = slices.DeleteFunc(p.Messages, func(m tg.MessageClass) bool { return m.GetID() >= beforeID })
	}
	slices.SortFunc(p.Messages, func(x, y tg.MessageClass) int { return y.GetID() - x.GetID() })
	return p, nil
}

// A negative add_offset of -limit moves the window to the messages right above
// offset_id, which gives contiguous forward paging from a cursor.
func (a *API) HistoryAfter(ctx context.Context, peer tg.InputPeerClass, afterID, limit int) (Page, error) {
	res, err := a.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer: peer, OffsetID: afterID + 1, AddOffset: -limit, Limit: limit, MinID: afterID,
	})
	if err != nil {
		return Page{}, err
	}
	p, err := toPage(res)
	if err != nil {
		return Page{}, err
	}
	p.Messages = slices.DeleteFunc(p.Messages, func(m tg.MessageClass) bool { return m.GetID() <= afterID })
	slices.SortFunc(p.Messages, func(x, y tg.MessageClass) int { return x.GetID() - y.GetID() })
	return p, nil
}

// HistoryCount returns the chat's total message count from a one-message page.
func (a *API) HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error) {
	res, err := a.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1})
	if err != nil {
		return 0, err
	}
	return messageCount(res)
}

func messageCount(res tg.MessagesMessagesClass) (int, error) {
	switch r := res.(type) {
	case *tg.MessagesMessagesSlice:
		return r.Count, nil
	case *tg.MessagesChannelMessages:
		return r.Count, nil
	case *tg.MessagesMessages:
		return len(r.Messages), nil
	}
	return 0, fmt.Errorf("unexpected history result %T", res)
}

// TypeCounts is the number of messages of each media kind in a chat.
type TypeCounts struct {
	Photos, Videos, Files, Voice, Round, Music, GIFs, Links int
}

// SearchCounters counts a chat's messages per media kind in one request.
func (a *API) SearchCounters(ctx context.Context, peer tg.InputPeerClass) (TypeCounts, error) {
	res, err := a.raw.MessagesGetSearchCounters(ctx, &tg.MessagesGetSearchCountersRequest{
		Peer: peer,
		Filters: []tg.MessagesFilterClass{
			&tg.InputMessagesFilterPhotos{}, &tg.InputMessagesFilterVideo{}, &tg.InputMessagesFilterDocument{},
			&tg.InputMessagesFilterVoice{}, &tg.InputMessagesFilterRoundVideo{}, &tg.InputMessagesFilterMusic{},
			&tg.InputMessagesFilterGif{}, &tg.InputMessagesFilterURL{},
		},
	})
	if err != nil {
		return TypeCounts{}, err
	}
	var c TypeCounts
	for _, r := range res {
		switch r.Filter.(type) {
		case *tg.InputMessagesFilterPhotos:
			c.Photos = r.Count
		case *tg.InputMessagesFilterVideo:
			c.Videos = r.Count
		case *tg.InputMessagesFilterDocument:
			c.Files = r.Count
		case *tg.InputMessagesFilterVoice:
			c.Voice = r.Count
		case *tg.InputMessagesFilterRoundVideo:
			c.Round = r.Count
		case *tg.InputMessagesFilterMusic:
			c.Music = r.Count
		case *tg.InputMessagesFilterGif:
			c.GIFs = r.Count
		case *tg.InputMessagesFilterURL:
			c.Links = r.Count
		}
	}
	return c, nil
}

// TopicCount returns how many topics a forum has.
func (a *API) TopicCount(ctx context.Context, peer tg.InputPeerClass) (int, error) {
	res, err := a.raw.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{Peer: peer, Limit: 1})
	if err != nil {
		return 0, err
	}
	return res.Count, nil
}

// TopicMessageCount returns the message count of one forum topic.
func (a *API) TopicMessageCount(ctx context.Context, peer tg.InputPeerClass, topicID int) (int, error) {
	res, err := a.raw.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{Peer: peer, MsgID: topicID, Limit: 1})
	if err != nil {
		return 0, err
	}
	return messageCount(res)
}

func (a *API) PinnedIDs(ctx context.Context, peer tg.InputPeerClass) ([]int, error) {
	const limit = 100
	var ids []int
	offset := 0
	for {
		res, err := a.raw.MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer: peer, Filter: &tg.InputMessagesFilterPinned{}, OffsetID: offset, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		p, err := toPage(res)
		if err != nil {
			return nil, err
		}
		for _, m := range p.Messages {
			ids = append(ids, m.GetID())
			if offset == 0 || m.GetID() < offset {
				offset = m.GetID()
			}
		}
		if len(p.Messages) < limit {
			return ids, nil
		}
	}
}

func (a *API) ForumTopics(ctx context.Context, peer tg.InputPeerClass) ([]Topic, error) {
	req := &tg.MessagesGetForumTopicsRequest{Peer: peer, Limit: 100}
	var out []Topic
	for {
		res, err := a.raw.MessagesGetForumTopics(ctx, req)
		if err != nil {
			return nil, err
		}
		var last *tg.ForumTopic
		for _, tc := range res.Topics {
			t, ok := tc.(*tg.ForumTopic)
			if !ok {
				continue
			}
			out = append(out, Topic{ID: t.ID, Title: t.Title, Closed: t.Closed, Date: t.Date})
			last = t
		}
		if len(res.Topics) < req.Limit || last == nil {
			return out, nil
		}
		req.OffsetDate, req.OffsetID, req.OffsetTopic = last.Date, last.TopMessage, last.ID
	}
}

func (a *API) Message(ctx context.Context, peer tg.InputPeerClass, id int) (tg.MessageClass, error) {
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	var (
		res tg.MessagesMessagesClass
		err error
	)
	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		res, err = a.raw.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, ID: ids,
		})
	} else {
		res, err = a.raw.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, err
	}
	p, err := toPage(res)
	if err != nil {
		return nil, err
	}
	for _, m := range p.Messages {
		if _, empty := m.(*tg.MessageEmpty); !empty && m.GetID() == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("message %d not found", id)
}

func (a *API) FileChunk(ctx context.Context, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error) {
	res, err := a.raw.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: offset, Limit: limit})
	if err != nil {
		return nil, err
	}
	f, ok := res.(*tg.UploadFile)
	if !ok {
		return nil, fmt.Errorf("unexpected upload.getFile result %T", res)
	}
	return f.Bytes, nil
}

func toPage(res tg.MessagesMessagesClass) (Page, error) {
	m, ok := res.AsModified()
	if !ok {
		return Page{}, errors.New("unexpected messages.messagesNotModified")
	}
	return Page{Messages: m.GetMessages(), Users: m.GetUsers(), Chats: m.GetChats()}, nil
}
