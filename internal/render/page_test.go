package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

func at(day, hhmm string) int64 {
	t, _ := time.Parse("2006-01-02 15:04", day+" "+hhmm)
	return t.Unix()
}

func samplePage(t *testing.T) Page {
	t.Helper()
	msgs := []store.RenderMessage{
		{Message: store.Message{ID: 1, Date: at("2026-03-01", "09:00"), Text: "hello <world>", EditDate: 5, Pinned: true}, SenderName: "Ann"},
		{Message: store.Message{ID: 2, Date: at("2026-03-01", "09:05"), Text: "same page", ReplyToID: 1}, SenderName: "Bob"},
		{Message: store.Message{ID: 3, Date: at("2026-03-02", "10:00"), Text: "other page", ReplyToID: 40}, SenderName: "Bob"},
		{Message: store.Message{ID: 4, Date: at("2026-03-02", "10:01"), Text: "lost", ReplyToID: 99}, SenderName: "Bob"},
		{Message: store.Message{ID: 5, Date: at("2026-03-02", "10:02"), FwdFromJSON: `{"from_name":"Old Friend","date":1700000000}`}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "photo", Status: "done", LocalPath: "media/photo/7.jpg"}},
		{Message: store.Message{ID: 6, Date: at("2026-03-02", "10:03"), FwdFromJSON: `{"from_id":-1000000000008,"date":1700000000}`}, SenderName: "Ann", FwdName: "News",
			Media: &store.RenderMedia{Kind: "document", FileName: "lease 2026.pdf", Size: 2 << 20, Status: "done", LocalPath: "media/document/9.pdf"}},
		{Message: store.Message{ID: 7, Date: at("2026-03-02", "10:04")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "video", Status: "done", LocalPath: "media/video/8.mp4"}},
		{Message: store.Message{ID: 8, Date: at("2026-03-02", "10:05")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "voice", Status: "done", LocalPath: "media/voice/9.ogg"}},
		{Message: store.Message{ID: 9, Date: at("2026-03-02", "10:06")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "video", FileName: "big.mov", Status: "skipped", Error: "larger than max_file_size"}},
		{Message: store.Message{ID: 10, Date: at("2026-03-02", "10:07")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "photo", Status: "pending"}},
		{Message: store.Message{ID: 11, Date: at("2026-03-02", "10:08"), ServiceActionJSON: `{"type":"messageActionTopicCreate","data":{"Title":"Recipes"}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 12, Date: at("2026-03-02", "10:09"), ServiceActionJSON: `{"type":"messageActionPinMessage","data":{}}`}, SenderName: "Bob"},
		{Message: store.Message{ID: 13, Date: at("2026-03-02", "10:10"), Text: "nice", ReactionsJSON: `[{"emoji":"👍","count":3},{"custom_emoji_id":5,"count":2}]`}, SenderName: "Bob"},
		{Message: store.Message{ID: 14, Date: at("2026-03-02", "10:11")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "animation", Mime: "video/mp4", Status: "done", LocalPath: "media/gif/1.mp4"}},
		{Message: store.Message{ID: 15, Date: at("2026-03-02", "10:12")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "video_note", Mime: "video/mp4", Status: "done", LocalPath: "media/round/1.mp4"}},
		{Message: store.Message{ID: 16, Date: at("2026-03-02", "10:13")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "sticker", Mime: "application/x-tgsticker", Status: "done", LocalPath: "media/sticker/1.tgs"}},
		{Message: store.Message{ID: 17, Date: at("2026-03-02", "10:14")}, SenderName: "Ann",
			Media: &store.RenderMedia{Kind: "audio", FileName: "song.mp3", Mime: "audio/mpeg", Status: "done", LocalPath: "media/audio/1.mp3"}},
	}
	refs := map[int]store.MessageRef{
		1:  {ID: 1, TopicID: 160, SenderName: "Ann", Text: "hello <world>"},
		40: {ID: 40, TopicID: 1, SenderName: "Cy", Text: strings.Repeat("long ", 40)},
	}
	key := store.PageKey{ChatID: -1001, TopicID: 160, N: 3}
	return buildPage(pageInput{
		Key:      key,
		Total:    9,
		Dir:      "Family/Expenses",
		Messages: msgs,
		Refs:     refs,
		DirOf: func(chat int64, topic int) string {
			return map[int]string{1: "Family/General", 160: "Family/Expenses"}[topic]
		},
		Locate: func(chat int64, id int) (store.PageKey, bool) {
			switch id {
			case 1:
				return key, true
			case 40:
				return store.PageKey{ChatID: -1001, TopicID: 1, N: 2}, true
			}
			return store.PageKey{}, false
		},
		Loc:    time.UTC,
		Crumbs: []string{"Family", "Expenses"},
	})
}

func TestPageHTML(t *testing.T) {
	var b bytes.Buffer
	if err := writePageHTML(&b, samplePage(t)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		`<link rel="stylesheet" href="../../assets/style.css">`,
		`id="m1"`, "hello &lt;world&gt;", ">edited<", ">pinned<",
		`<h2 class="day" id="d2026-03-01">`, `<h2 class="day" id="d2026-03-02">`, "2026-03-01 – 2026-03-02",
		`<a href="p2.html">← older</a>`, `<a href="p4.html">newer →</a>`, `<a href="dates.html">Jump to date</a>`,
		`<a href="p1.html">1</a>`, `<span class="gap">…</span>`, `<b>3</b>`, `<a href="p5.html">5</a>`, `<a href="p9.html">9</a>`,
		`<a class="reply" href="#m1">`,
		`<a class="reply" href="../General/p2.html#m40">`,
		"reply to a message not in the archive",
		"Forwarded from Old Friend", "Forwarded from News",
		`<img loading="lazy" src="../../../media/photo/7.jpg"`,
		`href="../../../media/document/9.pdf"`, "lease 2026.pdf", "2.0 MB",
		`<video class="lazy" controls playsinline preload="none" data-src="../../../media/video/8.mp4">`,
		`<div class="voice">🎤 <audio class="lazy" controls preload="none" data-src="../../../media/voice/9.ogg">`,
		"⛔ video not downloaded: larger than max_file_size",
		`<a class="media" href="../../../media/gif/1.mp4"><video class="lazy" muted loop playsinline preload="none" data-src="../../../media/gif/1.mp4">`,
		`<video class="lazy round" playsinline preload="none" data-src="../../../media/round/1.mp4" tabindex="0"`,
		`<a class="file" href="../../../media/sticker/1.tgs">📄`,
		`<audio class="lazy" controls preload="none" data-src="../../../media/audio/1.mp3"></audio> <a class="file" href="../../../media/audio/1.mp3">song.mp3</a>`,
		"⏳ photo not downloaded yet",
		`<span class="local" data-href="../../../media/document/9.pdf">💾</span>`,
		`<script src="../../assets/updated.js" defer></script>`, `<span class="asof"></span>`,
		`<script src="nav.js" defer></script>`, `<header class="bar">`, `<form class="search" role="search" data-root="../../"`,
		`placeholder="Search this chat…"`, `name="all"`, `<span class="jump" data-page="3"></span>`,
		"created topic «Recipes»", "pinned a message",
		"👍 3", "★ 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(got, "<world>") {
		t.Error("message text not escaped")
	}
	if t.Failed() {
		t.Log(got)
	}
}

func TestPageMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := writePageMD(&b, samplePage(t)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"# Family › Expenses · page 3 of 9",
		"[← older](p2.md) · [dates](dates.md) · [newer →](p4.md)",
		"**Ann** · 09:00", "hello \\<world\\>", "^m1",
		"> Ann: hello \\<world\\>",
		"](../General/p2.md#^m40)",
		"![](../../../media/photo/7.jpg)",
		"[lease 2026.pdf](../../../media/document/9.pdf)",
		"_video not downloaded: larger than max\\_file\\_size_",
		"_created topic «Recipes»_",
		// A blank line ends the quote, or the reply's own text would be quoted too.
		"[↩](#^m1)\n\nsame page",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(got)
	}
}

func metaPage(t *testing.T) Page {
	t.Helper()
	msgs := []store.RenderMessage{
		{Message: store.Message{ID: 1, Date: at("2026-03-01", "09:00"), Text: "look https://example.com/a",
			MetaJSON: `{"webpage":{"url":"https://example.com/a","site_name":"Example","title":"A <title>","description":"Some text","photo_key":"photo:7"}}`},
			SenderName: "Ann", Media: &store.RenderMedia{Key: "photo:7", Kind: "photo", Status: "done", LocalPath: "media/photo/7.jpg"}},
		{Message: store.Message{ID: 2, Date: at("2026-03-01", "09:01"),
			MetaJSON: `{"geo":{"lat":44.8125,"long":20.4612},"venue":{"title":"Kafana","address":"Knez Mihailova 1"}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 3, Date: at("2026-03-01", "09:02"), MetaJSON: `{"geo":{"lat":1,"long":2},"live":{"period":900}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 4, Date: at("2026-03-01", "09:03"),
			MetaJSON: `{"contact":{"first_name":"Bob","last_name":"Lee","phone":"+381601234567"}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 5, Date: at("2026-03-01", "09:04"),
			MetaJSON: `{"poll":{"question":"Dacha?","answers":[{"text":"Yes","voters":3},{"text":"No","voters":1,"correct":true}],"total_voters":4,"closed":true,"quiz":true}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 6, Date: at("2026-03-01", "09:05"),
			MetaJSON: `{"todo":{"title":"Packing","items":[{"id":1,"title":"Passports","done_by":7,"done_at":1772355600},{"id":2,"title":"Chargers"}]}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 7, Date: at("2026-03-01", "09:06"), MetaJSON: `{"dice":{"emoji":"🎲","value":4}}`}, SenderName: "Ann"},
		{Message: store.Message{ID: 8, Date: at("2026-03-01", "09:07"), MetaJSON: `{"other":{"type":"messageMediaStory"}}`}, SenderName: "Ann"},
	}
	return buildPage(pageInput{Key: store.PageKey{ChatID: 10, N: 1}, Total: 1, Dir: "Mom", Messages: msgs,
		Locate: func(int64, int) (store.PageKey, bool) { return store.PageKey{}, false },
		Names:  map[int64]string{7: "Bob"}, Loc: time.UTC, Crumbs: []string{"Mom"}})
}

func TestMetaHTML(t *testing.T) {
	var b bytes.Buffer
	if err := writePageHTML(&b, metaPage(t)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		`<a class="card link" href="https://example.com/a" rel="noopener">`, `src="../../media/photo/7.jpg"`, "Example", "A &lt;title&gt;", "Some text",
		"📍 Kafana, Knez Mihailova 1", `href="https://www.google.com/maps/search/?api=1&amp;query=44.8125,20.4612"`,
		"live location (last point)",
		"👤 Bob Lee", `href="tel:&#43;381601234567"`,
		"📊 Dacha?", "Yes — 75% (3)", "No — 25% (1) ✓", "4 votes · quiz · closed",
		"☑ Passports", "Bob · 2026-03-01", "☐ Chargers",
		"🎲 4", "messageMediaStory",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The preview's picture belongs to the card, not shown again as a bare photo.
	if n := strings.Count(got, "media/photo/7.jpg"); n != 1 {
		t.Errorf("preview picture shown %d times", n)
	}
	if t.Failed() {
		t.Log(got)
	}
}

func TestMetaMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := writePageMD(&b, metaPage(t)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"> [**A \\<title\\>**](<https://example.com/a>) · Example", "> Some text", "> ![](../../media/photo/7.jpg)",
		"📍 Kafana, Knez Mihailova 1 — [44.8125, 20.4612](<https://www.google.com/maps/search/?api=1&query=44.8125,20.4612>)",
		"👤 Bob Lee · +381601234567",
		"**📊 Dacha?**", "- No — 25% (1) ✓", "_4 votes · quiz · closed_",
		"- [x] Passports — Bob · 2026-03-01", "- [ ] Chargers",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(got)
	}
}

func TestMetaSearchText(t *testing.T) {
	got := metaText(`{"venue":{"title":"Kafana","address":"Knez Mihailova 1"},"poll":{"question":"Dacha?","answers":[{"text":"Yes"}]},"todo":{"title":"Packing","items":[{"title":"Passports"}]},"webpage":{"title":"A","description":"B"}}`)
	for _, want := range []string{"Kafana", "Knez Mihailova 1", "Dacha?", "Yes", "Packing", "Passports", "A", "B"} {
		if !strings.Contains(got, want) {
			t.Errorf("search text %q lacks %q", got, want)
		}
	}
}

func TestMediaView(t *testing.T) {
	for _, c := range []struct{ kind, mime, want string }{
		{"photo", "image/jpeg", "img"},
		{"sticker", "image/webp", "img"},
		{"sticker", "video/webm", "loop"},
		{"sticker", "application/x-tgsticker", "file"},
		{"animation", "video/mp4", "loop"},
		{"animation", "image/gif", "img"},
		{"video", "video/mp4", "video"},
		{"video_note", "video/mp4", "round"},
		{"voice", "audio/ogg", "voice"},
		{"audio", "audio/mpeg", "audio"},
		{"document", "application/pdf", "file"},
	} {
		if got := mediaView(c.kind, c.mime); got != c.want {
			t.Errorf("mediaView(%q, %q) = %q, want %q", c.kind, c.mime, got, c.want)
		}
	}
}

func TestMediaEntryNamesVoiceBySender(t *testing.T) {
	it := store.MediaItem{ID: 5, Date: 1772400000, Sender: "Катерина",
		Media: &store.RenderMedia{Kind: "voice", Status: "done", LocalPath: "media/2026/voice/a.ogg"}}
	got := mediaEntries(it, "Mom/p1.html#m5", "", time.UTC)
	if len(got) != 1 || got[0].V != "voice" || got[0].K != "voice" || got[0].N != "Voice message from Катерина" {
		t.Fatalf("entries = %+v", got)
	}
	it.Sender, it.Media.Kind = "", "video_note"
	if got := mediaEntries(it, "Mom/p1.html#m5", "", time.UTC); len(got) != 1 || got[0].N != "Round video" || got[0].V != "round" {
		t.Fatalf("no sender: %+v", got)
	}
}
