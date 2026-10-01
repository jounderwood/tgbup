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
	}
	refs := map[int]store.MessageRef{
		1:  {ID: 1, TopicID: 160, Month: "2026-03", SenderName: "Ann", Text: "hello <world>"},
		40: {ID: 40, TopicID: 1, Month: "2026-02", SenderName: "Cy", Text: strings.Repeat("long ", 40)},
	}
	return buildPage(pageInput{
		Key:      store.PageKey{ChatID: -1001, TopicID: 160, Month: "2026-03"},
		Dir:      "Family/Expenses",
		Messages: msgs,
		Refs:     refs,
		DirOf: func(chat int64, topic int) string {
			return map[int]string{1: "Family/General", 160: "Family/Expenses"}[topic]
		},
		Loc:    time.UTC,
		Crumbs: []string{"Family", "Expenses"},
	})
}

func TestMonthHTML(t *testing.T) {
	var b bytes.Buffer
	if err := writeMonthHTML(&b, samplePage(t)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		`<link rel="stylesheet" href="../../assets/style.css">`,
		`id="m1"`, "hello &lt;world&gt;", ">edited<", ">pinned<",
		`2026-03-01`, `2026-03-02`,
		`<a class="reply" href="#m1">`,
		`<a class="reply" href="../General/2026-02.html#m40">`,
		"reply to a message not in the archive",
		"Forwarded from Old Friend", "Forwarded from News",
		`<img loading="lazy" src="../../../media/photo/7.jpg"`,
		`href="../../../media/document/9.pdf"`, "lease 2026.pdf", "2.0 MB",
		`<video controls preload="none" src="../../../media/video/8.mp4">`,
		`<audio controls preload="none" src="../../../media/voice/9.ogg">`,
		"video not downloaded: larger than max_file_size",
		"photo not downloaded yet",
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

func TestMonthMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := writeMonthMD(&b, samplePage(t)); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"# Family › Expenses · 2026-03",
		"**Ann** · 09:00", "hello \\<world\\>", "^m1",
		"> Ann: hello \\<world\\>",
		"](../General/2026-02.md#^m40)",
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
