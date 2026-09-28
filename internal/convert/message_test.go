package convert_test

import (
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbp/internal/convert"
)

func TestPeerIDs(t *testing.T) {
	cases := []struct {
		peer tg.PeerClass
		want int64
	}{
		{&tg.PeerUser{UserID: 5}, 5},
		{&tg.PeerChat{ChatID: 7}, -7},
		{&tg.PeerChannel{ChannelID: 777}, -1000000000777},
	}
	for _, c := range cases {
		if got := convert.PeerID(c.peer); got != c.want {
			t.Errorf("PeerID(%T) = %d, want %d", c.peer, got, c.want)
		}
	}
	if id, ok := convert.InputPeerID(&tg.InputPeerChannel{ChannelID: 777}); !ok || id != -1000000000777 {
		t.Errorf("InputPeerID = %d %v", id, ok)
	}
	if _, ok := convert.InputPeerID(&tg.InputPeerSelf{}); ok {
		t.Error("InputPeerSelf must not resolve")
	}
}

func TestRegularMessage(t *testing.T) {
	m := &tg.Message{ID: 42, PeerID: &tg.PeerChannel{ChannelID: 777}, Date: 1700000000, Message: "см. доку", Pinned: true}
	m.SetFromID(&tg.PeerUser{UserID: 5})
	m.SetEntities([]tg.MessageEntityClass{
		&tg.MessageEntityTextURL{Offset: 3, Length: 4, URL: "https://example.com/doc"},
		&tg.MessageEntityMentionName{Offset: 0, Length: 2, UserID: 9},
	})
	m.SetGroupedID(99)
	m.SetEditDate(1700000100)

	res, ok, err := convert.Message(m, convert.Options{SelfID: 1})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	got := res.Message
	if got.ChatID != -1000000000777 || got.ID != 42 || got.Date != 1700000000 || got.FromID != 5 {
		t.Errorf("ids = %+v", got)
	}
	if !got.Pinned || got.GroupedID != 99 || got.EditDate != 1700000100 || got.TopicID != 0 {
		t.Errorf("flags = %+v", got)
	}
	for _, want := range []string{`"url":"https://example.com/doc"`, `"type":"messageEntityTextUrl"`, `"user_id":9`} {
		if !strings.Contains(got.EntitiesJSON, want) {
			t.Errorf("entities %s missing %s", got.EntitiesJSON, want)
		}
	}
	if got.TLLayer != tg.Layer {
		t.Errorf("layer = %d", got.TLLayer)
	}
	decoded, err := tg.DecodeMessage(&bin.Buffer{Buf: got.Raw})
	if err != nil {
		t.Fatal(err)
	}
	if d := decoded.(*tg.Message); d.Message != "см. доку" || d.ID != 42 {
		t.Errorf("raw round-trip = %+v", d)
	}
}

func TestSenderResolution(t *testing.T) {
	in := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 50}}
	res, _, _ := convert.Message(in, convert.Options{SelfID: 1})
	if res.Message.FromID != 50 {
		t.Errorf("incoming private from = %d, want peer 50", res.Message.FromID)
	}
	out := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 50}, Out: true}
	res, _, _ = convert.Message(out, convert.Options{SelfID: 1})
	if res.Message.FromID != 1 {
		t.Errorf("outgoing from = %d, want self 1", res.Message.FromID)
	}
}

func forumReply(msgID, topID int, forum bool) *tg.MessageReplyHeader {
	h := &tg.MessageReplyHeader{ForumTopic: forum}
	if msgID != 0 {
		h.SetReplyToMsgID(msgID)
	}
	if topID != 0 {
		h.SetReplyToTopID(topID)
	}
	return h
}

func TestThreading(t *testing.T) {
	cases := []struct {
		name           string
		forum          bool
		reply          tg.MessageReplyHeaderClass
		topic, replyTo int
	}{
		{"plain chat reply", false, forumReply(10, 0, false), 0, 10},
		{"forum, no reply header → General", true, nil, 1, 0},
		{"forum, post in topic root", true, forumReply(100, 0, true), 100, 0},
		{"forum, reply inside topic", true, forumReply(150, 100, true), 100, 150},
		{"forum, reply without forum flag → General", true, forumReply(20, 0, false), 1, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &tg.Message{ID: 200, PeerID: &tg.PeerChannel{ChannelID: 1}}
			if c.reply != nil {
				m.SetReplyTo(c.reply)
			}
			res, _, err := convert.Message(m, convert.Options{IsForum: c.forum})
			if err != nil {
				t.Fatal(err)
			}
			if res.Message.TopicID != c.topic || res.Message.ReplyToID != c.replyTo {
				t.Errorf("topic=%d reply=%d, want %d %d", res.Message.TopicID, res.Message.ReplyToID, c.topic, c.replyTo)
			}
		})
	}
}

func TestServiceMessages(t *testing.T) {
	pin := &tg.MessageService{ID: 300, PeerID: &tg.PeerChannel{ChannelID: 1}, Date: 5, Action: &tg.MessageActionPinMessage{}}
	pin.SetFromID(&tg.PeerUser{UserID: 5})
	pin.SetReplyTo(forumReply(42, 0, false))
	res, ok, err := convert.Message(pin, convert.Options{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !strings.Contains(res.Message.ServiceActionJSON, `"type":"messageActionPinMessage"`) {
		t.Errorf("action = %s", res.Message.ServiceActionJSON)
	}
	if res.Message.ReplyToID != 42 || res.Message.FromID != 5 {
		t.Errorf("pin target/from = %d/%d", res.Message.ReplyToID, res.Message.FromID)
	}

	create := &tg.MessageService{ID: 500, PeerID: &tg.PeerChannel{ChannelID: 1}, Action: &tg.MessageActionTopicCreate{Title: "Рецепты"}}
	res, _, _ = convert.Message(create, convert.Options{IsForum: true})
	if res.Message.TopicID != 500 {
		t.Errorf("topic create topic = %d, want own id", res.Message.TopicID)
	}
	if !strings.Contains(res.Message.ServiceActionJSON, "Рецепты") {
		t.Errorf("action data lost: %s", res.Message.ServiceActionJSON)
	}
}

func TestForwardAndReactions(t *testing.T) {
	m := &tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 3}}
	fwd := tg.MessageFwdHeader{Date: 1600000000}
	fwd.SetFromID(&tg.PeerChannel{ChannelID: 8})
	fwd.SetChannelPost(77)
	m.SetFwdFrom(fwd)
	m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 3},
		{Reaction: &tg.ReactionCustomEmoji{DocumentID: 123}, Count: 1},
	}})
	res, _, err := convert.Message(m, convert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message.FwdFromJSON, `"from_id":-1000000000008`) || !strings.Contains(res.Message.FwdFromJSON, `"channel_post":77`) {
		t.Errorf("fwd = %s", res.Message.FwdFromJSON)
	}
	if !strings.Contains(res.Message.ReactionsJSON, `"emoji":"👍","count":3`) || !strings.Contains(res.Message.ReactionsJSON, `"custom_emoji_id":123`) {
		t.Errorf("reactions = %s", res.Message.ReactionsJSON)
	}
}

func TestEmptyMessageSkipped(t *testing.T) {
	_, ok, err := convert.Message(&tg.MessageEmpty{ID: 5}, convert.Options{})
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestUsers(t *testing.T) {
	us := convert.Users([]tg.UserClass{
		&tg.User{ID: 1, FirstName: "A", Username: "a", Bot: true},
		&tg.User{ID: 2, Min: true},
		&tg.UserEmpty{ID: 3},
	})
	if len(us) != 2 || !us[0].IsBot || us[0].Username != "a" || !us[1].Partial {
		t.Fatalf("users = %+v", us)
	}
}
