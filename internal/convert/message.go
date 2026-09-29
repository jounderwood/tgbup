package convert

import (
	"encoding/json"
	"fmt"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

type Options struct {
	IsForum bool
	SelfID  int64
}

type Result struct {
	Message store.Message
	Media   []store.Media
}

func Message(mc tg.MessageClass, opts Options) (Result, bool, error) {
	switch m := mc.(type) {
	case *tg.Message:
		return regular(m, opts)
	case *tg.MessageService:
		return service(m, opts)
	default:
		return Result{}, false, nil
	}
}

func Encode(mc tg.MessageClass) ([]byte, error) {
	var b bin.Buffer
	if err := mc.Encode(&b); err != nil {
		return nil, err
	}
	return b.Copy(), nil
}

func regular(m *tg.Message, opts Options) (Result, bool, error) {
	raw, err := Encode(m)
	if err != nil {
		return Result{}, false, fmt.Errorf("encode message %d: %w", m.ID, err)
	}
	from, hasFrom := m.GetFromID()
	out := store.Message{
		ChatID:    PeerID(m.PeerID),
		ID:        m.ID,
		Date:      int64(m.Date),
		FromID:    senderID(from, hasFrom, m.Out, m.PeerID, opts.SelfID),
		Text:      m.Message,
		GroupedID: m.GroupedID,
		Pinned:    m.Pinned,
		EditDate:  int64(m.EditDate),
		Raw:       raw,
		TLLayer:   tg.Layer,
	}
	reply, _ := m.GetReplyTo()
	out.TopicID, out.ReplyToID = threading(opts.IsForum, reply, m.ID, false)

	if len(m.Entities) > 0 {
		if out.EntitiesJSON, err = entitiesJSON(m.Entities); err != nil {
			return Result{}, false, err
		}
	}
	if fwd, ok := m.GetFwdFrom(); ok {
		if out.FwdFromJSON, err = forwardJSON(fwd); err != nil {
			return Result{}, false, err
		}
	}
	if r, ok := m.GetReactions(); ok && len(r.Results) > 0 {
		if out.ReactionsJSON, err = reactionsJSON(r); err != nil {
			return Result{}, false, err
		}
	}
	res := Result{Message: out}
	if md, ok := ExtractMedia(m); ok {
		res.Media = []store.Media{md}
		res.Message.MediaKeys = []string{md.Key}
	}
	return res, true, nil
}

func service(m *tg.MessageService, opts Options) (Result, bool, error) {
	raw, err := Encode(m)
	if err != nil {
		return Result{}, false, fmt.Errorf("encode service message %d: %w", m.ID, err)
	}
	action, err := json.Marshal(struct {
		Type string                `json:"type"`
		Data tg.MessageActionClass `json:"data"`
	}{m.Action.TypeName(), m.Action})
	if err != nil {
		action, _ = json.Marshal(map[string]string{"type": m.Action.TypeName()})
	}
	_, createsTopic := m.Action.(*tg.MessageActionTopicCreate)
	from, hasFrom := m.GetFromID()
	out := store.Message{
		ChatID:            PeerID(m.PeerID),
		ID:                m.ID,
		Date:              int64(m.Date),
		FromID:            senderID(from, hasFrom, m.Out, m.PeerID, opts.SelfID),
		ServiceActionJSON: string(action),
		Raw:               raw,
		TLLayer:           tg.Layer,
	}
	reply, _ := m.GetReplyTo()
	out.TopicID, out.ReplyToID = threading(opts.IsForum, reply, m.ID, createsTopic)
	return Result{Message: out}, true, nil
}

func senderID(from tg.PeerClass, hasFrom, out bool, peer tg.PeerClass, selfID int64) int64 {
	if hasFrom {
		return PeerID(from)
	}
	if out {
		return selfID
	}
	return PeerID(peer)
}

// In forums reply_to_top_id is set only when replying to a non-root message;
// otherwise reply_to_msg_id is the topic root itself, not a real reply.
func threading(isForum bool, rc tg.MessageReplyHeaderClass, msgID int, createsTopic bool) (topicID, replyToID int) {
	h, _ := rc.(*tg.MessageReplyHeader)
	if h != nil {
		replyToID, _ = h.GetReplyToMsgID()
	}
	if !isForum {
		return 0, replyToID
	}
	if createsTopic {
		return msgID, 0
	}
	if h == nil || !h.ForumTopic {
		return 1, replyToID
	}
	if top, ok := h.GetReplyToTopID(); ok {
		return top, replyToID
	}
	return replyToID, 0
}

// Offsets and lengths are in UTF-16 code units, as in the Telegram API.
type entityJSON struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	UserID int64  `json:"user_id,omitempty"`
}

func entitiesJSON(es []tg.MessageEntityClass) (string, error) {
	out := make([]entityJSON, 0, len(es))
	for _, e := range es {
		j := entityJSON{Type: e.TypeName(), Offset: e.GetOffset(), Length: e.GetLength()}
		switch v := e.(type) {
		case *tg.MessageEntityTextURL:
			j.URL = v.URL
		case *tg.MessageEntityMentionName:
			j.UserID = v.UserID
		}
		out = append(out, j)
	}
	b, err := json.Marshal(out)
	return string(b), err
}

type fwdJSON struct {
	FromID         int64  `json:"from_id,omitempty"`
	FromName       string `json:"from_name,omitempty"`
	Date           int    `json:"date"`
	ChannelPost    int    `json:"channel_post,omitempty"`
	PostAuthor     string `json:"post_author,omitempty"`
	SavedFromPeer  int64  `json:"saved_from_peer,omitempty"`
	SavedFromMsgID int    `json:"saved_from_msg_id,omitempty"`
}

func forwardJSON(h tg.MessageFwdHeader) (string, error) {
	j := fwdJSON{FromName: h.FromName, Date: h.Date, ChannelPost: h.ChannelPost, PostAuthor: h.PostAuthor}
	if p, ok := h.GetFromID(); ok {
		j.FromID = PeerID(p)
	}
	if p, ok := h.GetSavedFromPeer(); ok {
		j.SavedFromPeer = PeerID(p)
	}
	j.SavedFromMsgID, _ = h.GetSavedFromMsgID()
	b, err := json.Marshal(j)
	return string(b), err
}

type reactionJSON struct {
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID int64  `json:"custom_emoji_id,omitempty"`
	Paid          bool   `json:"paid,omitempty"`
	Count         int    `json:"count"`
}

func reactionsJSON(r tg.MessageReactions) (string, error) {
	out := make([]reactionJSON, 0, len(r.Results))
	for _, rc := range r.Results {
		j := reactionJSON{Count: rc.Count}
		switch v := rc.Reaction.(type) {
		case *tg.ReactionEmoji:
			j.Emoji = v.Emoticon
		case *tg.ReactionCustomEmoji:
			j.CustomEmojiID = v.DocumentID
		case *tg.ReactionPaid:
			j.Paid = true
		}
		out = append(out, j)
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func Peers(cs []tg.ChatClass) []store.Peer {
	out := make([]store.Peer, 0, len(cs))
	for _, cc := range cs {
		switch c := cc.(type) {
		case *tg.Chat:
			out = append(out, store.Peer{ID: ChatID(c.ID), Title: c.Title})
		case *tg.ChatForbidden:
			out = append(out, store.Peer{ID: ChatID(c.ID), Title: c.Title})
		case *tg.Channel:
			out = append(out, store.Peer{ID: ChannelID(c.ID), Title: c.Title, Username: c.Username})
		case *tg.ChannelForbidden:
			out = append(out, store.Peer{ID: ChannelID(c.ID), Title: c.Title})
		}
	}
	return out
}

func Users(us []tg.UserClass) []store.User {
	out := make([]store.User, 0, len(us))
	for _, uc := range us {
		u, ok := uc.(*tg.User)
		if !ok {
			continue
		}
		out = append(out, store.User{
			ID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username, IsBot: u.Bot, Partial: u.Min,
		})
	}
	return out
}
