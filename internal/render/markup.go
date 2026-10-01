package render

import (
	"encoding/json"
	"html/template"
	"net/url"
	"slices"
	"strings"
	"unicode/utf16"
)

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url"`
	UserID int64  `json:"user_id"`
}

// span is an entity resolved against the text, in UTF-16 units as Telegram counts.
type span struct {
	entity
	start, end int
	text       string // the whole entity's text, for links built from it
}

type segment struct {
	text   string
	active []*span // outermost first
}

// segments cuts text at every entity boundary; each piece carries the
// entities covering it. Overlapping entities are reopened per piece, so the
// output stays well-formed whatever Telegram sends.
func segments(text, entitiesJSON string) []segment {
	units := utf16.Encode([]rune(text))
	var ents []entity
	if entitiesJSON != "" {
		_ = json.Unmarshal([]byte(entitiesJSON), &ents) // bad JSON → plain text
	}
	var spans []*span
	cuts := []int{0, len(units)}
	for _, e := range ents {
		start := min(max(e.Offset, 0), len(units))
		end := min(max(e.Offset+e.Length, start), len(units))
		if start == end {
			continue
		}
		spans = append(spans, &span{entity: e, start: start, end: end, text: string(utf16.Decode(units[start:end]))})
		cuts = append(cuts, start, end)
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	slices.SortStableFunc(spans, func(a, b *span) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	var out []segment
	for i := 0; i+1 < len(cuts); i++ {
		a, b := cuts[i], cuts[i+1]
		seg := segment{text: string(utf16.Decode(units[a:b]))}
		for _, s := range spans {
			if s.start <= a && s.end >= b {
				seg.active = append(seg.active, s)
			}
		}
		out = append(out, seg)
	}
	return out
}

// walk emits open/close markers around segment texts, closing and reopening
// only what changes between neighbouring segments.
func walk(segs []segment, open, close func(*span) string, body func(segment) string) string {
	var (
		b     strings.Builder
		stack []*span
	)
	for _, seg := range segs {
		common := 0
		for common < len(stack) && common < len(seg.active) && stack[common] == seg.active[common] {
			common++
		}
		for i := len(stack) - 1; i >= common; i-- {
			b.WriteString(close(stack[i]))
		}
		stack = stack[:common]
		for _, s := range seg.active[common:] {
			b.WriteString(open(s))
			stack = append(stack, s)
		}
		b.WriteString(body(seg))
	}
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteString(close(stack[i]))
	}
	return b.String()
}

// HTMLText renders message text with its formatting; all text is escaped and
// links are limited to safe schemes.
func HTMLText(text, entitiesJSON string) template.HTML {
	open := func(s *span) string {
		switch s.Type {
		case "messageEntityBold":
			return "<b>"
		case "messageEntityItalic":
			return "<i>"
		case "messageEntityUnderline":
			return "<u>"
		case "messageEntityStrike":
			return "<s>"
		case "messageEntityCode":
			return "<code>"
		case "messageEntityPre":
			return "<pre>"
		case "messageEntitySpoiler":
			return `<span class="spoiler">`
		case "messageEntityBlockquote":
			return "<blockquote>"
		}
		if href, ok := entityHref(s); ok {
			return `<a href="` + template.HTMLEscapeString(href) + `" rel="noopener">`
		}
		return ""
	}
	close := func(s *span) string {
		switch s.Type {
		case "messageEntityBold":
			return "</b>"
		case "messageEntityItalic":
			return "</i>"
		case "messageEntityUnderline":
			return "</u>"
		case "messageEntityStrike":
			return "</s>"
		case "messageEntityCode":
			return "</code>"
		case "messageEntityPre":
			return "</pre>"
		case "messageEntitySpoiler":
			return "</span>"
		case "messageEntityBlockquote":
			return "</blockquote>"
		}
		if _, ok := entityHref(s); ok {
			return "</a>"
		}
		return ""
	}
	// Safe as HTML: every piece of text is escaped and tags come only from the switches above.
	return template.HTML(walk(segments(text, entitiesJSON), open, close, func(seg segment) string {
		return template.HTMLEscapeString(seg.text)
	}))
}

// entityHref is the link target of a link-like entity, if it is safe.
func entityHref(s *span) (string, bool) {
	switch s.Type {
	case "messageEntityTextUrl":
		return safeURL(s.URL)
	case "messageEntityUrl":
		u := s.text
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		return safeURL(u)
	case "messageEntityEmail":
		return safeURL("mailto:" + s.text)
	case "messageEntityPhone":
		return safeURL("tel:" + s.text)
	case "messageEntityMention":
		return safeURL("https://t.me/" + strings.TrimPrefix(s.text, "@"))
	case "messageEntityMentionName":
		return safeURL("tg://user?id=" + itoa64(s.UserID))
	}
	return "", false
}

func safeURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "tel", "tg":
		return u.String(), true
	}
	return "", false
}

var mdEscaper = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "`", "\\`", "<", `\<`, ">", `\>`, "|", `\|`, "~", `\~`, "#", `\#`)

// MarkdownText renders message text as Markdown for Obsidian.
func MarkdownText(text, entitiesJSON string) string {
	open := func(s *span) string {
		switch s.Type {
		case "messageEntityBold":
			return "**"
		case "messageEntityItalic":
			return "_"
		case "messageEntityStrike":
			return "~~"
		case "messageEntityCode":
			return "`"
		case "messageEntityPre":
			return "\n```\n"
		case "messageEntityTextUrl", "messageEntityMentionName":
			if _, ok := entityHref(s); ok {
				return "["
			}
		}
		return ""
	}
	close := func(s *span) string {
		switch s.Type {
		case "messageEntityBold":
			return "**"
		case "messageEntityItalic":
			return "_"
		case "messageEntityStrike":
			return "~~"
		case "messageEntityCode":
			return "`"
		case "messageEntityPre":
			return "\n```\n"
		case "messageEntityTextUrl", "messageEntityMentionName":
			if href, ok := entityHref(s); ok {
				return "](<" + href + ">)"
			}
		}
		return ""
	}
	return walk(segments(text, entitiesJSON), open, close, func(seg segment) string {
		for _, s := range seg.active {
			switch s.Type {
			case "messageEntityCode", "messageEntityPre", "messageEntityUrl", "messageEntityEmail":
				return seg.text // literal: code, and autolinks Obsidian detects itself
			}
		}
		return mdEscaper.Replace(seg.text)
	})
}
