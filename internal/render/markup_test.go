package render

import (
	"encoding/json"
	"testing"
)

type ent struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	UserID int64  `json:"user_id,omitempty"`
}

func entities(es ...ent) string {
	b, _ := json.Marshal(es)
	return string(b)
}

func TestHTMLText(t *testing.T) {
	cases := []struct {
		name, text, ents, want string
	}{
		{"plain escaped", "a <b> & \"c\"", "", "a &lt;b&gt; &amp; &#34;c&#34;"},
		{"bold", "hello world", entities(ent{"messageEntityBold", 6, 5, "", 0}), "hello <b>world</b>"},
		{"nested", "abcdef", entities(ent{"messageEntityBold", 0, 6, "", 0}, ent{"messageEntityItalic", 2, 2, "", 0}),
			"<b>ab<i>cd</i>ef</b>"},
		{"overlapping", "abcdef", entities(ent{"messageEntityBold", 0, 4, "", 0}, ent{"messageEntityItalic", 2, 4, "", 0}),
			"<b>ab<i>cd</i></b><i>ef</i>"},
		// 🏡 is two UTF-16 units: offsets after it must not shift or split it.
		{"emoji before", "🏡 home", entities(ent{"messageEntityBold", 3, 4, "", 0}), "🏡 <b>home</b>"},
		{"emoji inside", "a🏡b", entities(ent{"messageEntityItalic", 1, 2, "", 0}), "a<i>🏡</i>b"},
		{"text url", "docs", entities(ent{"messageEntityTextUrl", 0, 4, "https://example.com/doc?a=1&b=2", 0}),
			`<a href="https://example.com/doc?a=1&amp;b=2" rel="noopener">docs</a>`},
		{"javascript url dropped", "x", entities(ent{"messageEntityTextUrl", 0, 1, "javascript:alert(1)", 0}), "x"},
		{"bare url", "see example.com now", entities(ent{"messageEntityUrl", 4, 11, "", 0}),
			`see <a href="https://example.com" rel="noopener">example.com</a> now`},
		{"email", "a@b.c", entities(ent{"messageEntityEmail", 0, 5, "", 0}), `<a href="mailto:a@b.c" rel="noopener">a@b.c</a>`},
		{"mention", "hi @ann", entities(ent{"messageEntityMention", 3, 4, "", 0}), `hi <a href="https://t.me/ann" rel="noopener">@ann</a>`},
		{"mention name", "Ann", entities(ent{"messageEntityMentionName", 0, 3, "", 5}), `<a href="tg://user?id=5" rel="noopener">Ann</a>`},
		{"code escapes", "<x>", entities(ent{"messageEntityCode", 0, 3, "", 0}), "<code>&lt;x&gt;</code>"},
		{"pre", "a\nb", entities(ent{"messageEntityPre", 0, 3, "", 0}), "<pre>a\nb</pre>"},
		{"spoiler", "secret", entities(ent{"messageEntitySpoiler", 0, 6, "", 0}), `<span class="spoiler">secret</span>`},
		{"out of range clamped", "ab", entities(ent{"messageEntityBold", 1, 50, "", 0}), "a<b>b</b>"},
		{"bad json ignored", "ab", "{", "ab"},
	}
	for _, c := range cases {
		if got := string(HTMLText(c.text, c.ents)); got != c.want {
			t.Errorf("%s: got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestMarkdownText(t *testing.T) {
	cases := []struct {
		name, text, ents, want string
	}{
		{"escapes", "a*b_c [d]", "", `a\*b\_c \[d\]`},
		{"bold italic", "ab cd", entities(ent{"messageEntityBold", 0, 2, "", 0}, ent{"messageEntityItalic", 3, 2, "", 0}), "**ab** _cd_"},
		{"link", "docs", entities(ent{"messageEntityTextUrl", 0, 4, "https://e.com/a_(b)", 0}), `[docs](<https://e.com/a_(b)>)`},
		{"code", "x*y", entities(ent{"messageEntityCode", 0, 3, "", 0}), "`x*y`"},
		{"pre", "a\nb", entities(ent{"messageEntityPre", 0, 3, "", 0}), "\n```\na\nb\n```\n"},
		{"emoji", "🏡 home", entities(ent{"messageEntityStrike", 3, 4, "", 0}), "🏡 ~~home~~"},
	}
	for _, c := range cases {
		if got := MarkdownText(c.text, c.ents); got != c.want {
			t.Errorf("%s: got %q\nwant %q", c.name, got, c.want)
		}
	}
}
