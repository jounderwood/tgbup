package convert_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/jounderwood/tgbup/internal/convert"
)

func withMedia(mm tg.MessageMediaClass) *tg.Message {
	m := &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 1}, Message: "x"}
	m.SetMedia(mm)
	return m
}

func meta(t *testing.T, m *tg.Message) map[string]any {
	t.Helper()
	res, ok, err := convert.Message(m, convert.Options{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if res.Message.MetaJSON == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Message.MetaJSON), &out); err != nil {
		t.Fatalf("meta_json %q: %v", res.Message.MetaJSON, err)
	}
	return out
}

func text(s string) tg.TextWithEntities { return tg.TextWithEntities{Text: s} }

func TestMessageMeta(t *testing.T) {
	answers := []tg.PollAnswerClass{&tg.PollAnswer{Text: text("Yes"), Option: []byte{0}}, &tg.PollAnswer{Text: text("No"), Option: []byte{1}}}
	poll := &tg.MessageMediaPoll{
		Poll:    tg.Poll{Question: text("Dacha on Saturday?"), Answers: answers, Closed: true},
		Results: tg.PollResults{Results: []tg.PollAnswerVoters{{Option: []byte{0}, Voters: 3}, {Option: []byte{1}, Voters: 1, Correct: true}}, TotalVoters: 4},
	}
	todo := &tg.MessageMediaToDo{
		Todo:        tg.TodoList{Title: text("Packing"), OthersCanComplete: true, List: []tg.TodoItem{{ID: 1, Title: text("Passports")}, {ID: 2, Title: text("Chargers")}}},
		Completions: []tg.TodoCompletion{{ID: 1, CompletedBy: &tg.PeerUser{UserID: 7}, Date: 1700000000}},
	}
	cases := []struct {
		name string
		mm   tg.MessageMediaClass
		want map[string]any
	}{
		{"geo", &tg.MessageMediaGeo{Geo: &tg.GeoPoint{Lat: 48.8, Long: 2.29}},
			map[string]any{"geo": map[string]any{"lat": 48.8, "long": 2.29}}},
		{"venue", &tg.MessageMediaVenue{Geo: &tg.GeoPoint{Lat: 1, Long: 2}, Title: "Cafe", Address: "Rue de Rivoli 1", Provider: "foursquare"},
			map[string]any{"geo": map[string]any{"lat": 1.0, "long": 2.0}, "venue": map[string]any{"title": "Cafe", "address": "Rue de Rivoli 1", "provider": "foursquare"}}},
		{"live", &tg.MessageMediaGeoLive{Geo: &tg.GeoPoint{Lat: 3, Long: 4}, Period: 900},
			map[string]any{"geo": map[string]any{"lat": 3.0, "long": 4.0}, "live": map[string]any{"period": 900.0}}},
		{"contact", &tg.MessageMediaContact{FirstName: "Ann", LastName: "Lee", PhoneNumber: "+381601234567", UserID: 9},
			map[string]any{"contact": map[string]any{"first_name": "Ann", "last_name": "Lee", "phone": "+381601234567", "user_id": 9.0}}},
		{"poll", poll, map[string]any{"poll": map[string]any{
			"question": "Dacha on Saturday?", "total_voters": 4.0, "closed": true,
			"answers": []any{map[string]any{"text": "Yes", "voters": 3.0}, map[string]any{"text": "No", "voters": 1.0, "correct": true}},
		}}},
		{"todo", todo, map[string]any{"todo": map[string]any{
			"title": "Packing", "others_can_complete": true,
			"items": []any{map[string]any{"id": 1.0, "title": "Passports", "done_by": 7.0, "done_at": 1700000000.0}, map[string]any{"id": 2.0, "title": "Chargers"}},
		}}},
		{"dice", &tg.MessageMediaDice{Emoticon: "🎲", Value: 4}, map[string]any{"dice": map[string]any{"emoji": "🎲", "value": 4.0}}},
		{"other", &tg.MessageMediaStory{ID: 3, Peer: &tg.PeerUser{UserID: 1}}, map[string]any{"other": map[string]any{"type": "messageMediaStory"}}},
	}
	for _, c := range cases {
		if got := meta(t, withMedia(c.mm)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: meta = %#v\nwant %#v", c.name, got, c.want)
		}
	}
	if got := meta(t, &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 1}, Message: "plain"}); got != nil {
		t.Errorf("plain message has meta %v", got)
	}
}

// The link preview's picture is downloaded like any photo and referenced from the meta.
func TestWebPagePreview(t *testing.T) {
	p := photoMessage().Media.(*tg.MessageMediaPhoto).Photo
	wp := &tg.WebPage{ID: 1, URL: "https://example.com/a", DisplayURL: "example.com/a", Type: "article",
		SiteName: "Example", Title: "A title", Description: "Some text"}
	wp.SetPhoto(p)
	m := withMedia(&tg.MessageMediaWebPage{Webpage: wp})
	got := meta(t, m)
	want := map[string]any{"webpage": map[string]any{"url": "https://example.com/a", "display_url": "example.com/a", "type": "article",
		"site_name": "Example", "title": "A title", "description": "Some text", "photo_key": "photo:777"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("meta = %#v", got)
	}
	md, ok := convert.ExtractMedia(m)
	if !ok || md.Key != "photo:777" || md.Kind != "photo" {
		t.Fatalf("preview photo = %+v %v", md, ok)
	}
	if loc, err := convert.FileLocation(m); err != nil || loc.(*tg.InputPhotoFileLocation).ID != 777 {
		t.Fatalf("location = %v %v", loc, err)
	}
	// A preview Telegram has not built yet carries nothing worth keeping.
	if got := meta(t, withMedia(&tg.MessageMediaWebPage{Webpage: &tg.WebPagePending{ID: 1, URL: "https://x"}})); got["webpage"].(map[string]any)["url"] != "https://x" {
		t.Errorf("pending preview = %#v", got)
	}
}
