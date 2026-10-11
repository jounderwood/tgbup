package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jounderwood/tgbup/internal/convert"
	"github.com/jounderwood/tgbup/internal/store"
)

// Page is one page of a chat's or forum topic's thread.
type Page struct {
	Title  string // "Family › Expenses · page 7 of 38"
	Crumbs []string
	ChatID int64
	Topic  string // the thread inside the chat (forum topic, "before upgrade"); "" for none
	N      int
	Total  int
	Range  string // "2026-03-01 – 2026-03-29"
	// Root leads from the page to the html/ (or md/) root, Archive to the archive root.
	Root, Archive string
	Prev, Next    string // neighbouring pages without extension ("p6"); "" when none
	Pager         []PagerItem
	Days          []Day
}

type PagerItem struct {
	Label   string
	Href    string // without extension; "" for the current page and gaps
	Current bool
	Gap     bool
}

type Day struct {
	Date     string
	Anchor   string // "d2026-03-14"
	Messages []PageMessage
}

type PageMessage struct {
	ID        int
	Time      string
	Sender    string
	HTML      template.HTML
	Markdown  string
	Edited    bool
	Pinned    bool
	Reply     *Reply
	Forward   string
	Media     *PageMedia
	Meta      *PageMeta
	Service   string
	Reactions string
	// Versions are the message's versions before its edits, oldest first.
	Versions []PageVersion
}

// PageVersion is a message as it was before an edit.
type PageVersion struct {
	When     string // "as sent 14:02" or "edited 14:05"
	Seen     string // until when tgbup saw it, for the tooltip
	HTML     template.HTML
	Markdown string
	// Attachment: it had another attachment, which the edit replaced.
	Attachment bool
}

type Reply struct {
	// Page is the target page relative to this one, without extension; "" when
	// it is this page.
	Page   string
	ID     int
	Sender string
	Text   string
	// Missing: the target is not in the archive. NoLink: it is, but not in
	// this render (an export of part of a chat), so it is quoted without a link.
	Missing bool
	NoLink  bool
}

type PageMedia struct {
	Kind string
	Href string // relative to the page; "" when the file is not downloaded
	Name string
	Size string
	Note string // why the file is not there
	// State is why it is not there: pending, skipped or failed.
	State string
	// View is how the file is shown: img, video, loop (a GIF or a video
	// sticker, playing silently), round, voice, audio or file.
	View string
}

type pageInput struct {
	Key      store.PageKey
	Total    int
	Dir      string // the page's folder inside html/ and md/, slash-separated
	Messages []store.RenderMessage
	Refs     map[int]store.MessageRef
	// DirOf gives the folder of another thread of the same chat.
	DirOf func(chat int64, topic int) string
	// Locate finds the page a message is drawn on; false when it is not in
	// this render (an export may leave messages out).
	Locate func(chat int64, id int) (store.PageKey, bool)
	// Names resolves ids met in meta (who completed a checklist item).
	Names map[int64]string
	// Versions are the messages' versions before edits.
	Versions map[int][]store.MessageVersion
	Loc      *time.Location
	Crumbs   []string
}

func pageName(n int) string { return "p" + strconv.Itoa(n) }

func buildPage(in pageInput) Page {
	depth := strings.Count(in.Dir, "/") + 1
	p := Page{
		Title:   fmt.Sprintf("%s · page %d of %d", strings.Join(in.Crumbs, " › "), in.Key.N, in.Total),
		Crumbs:  in.Crumbs,
		ChatID:  in.Key.ChatID,
		N:       in.Key.N,
		Topic:   strings.Join(in.Crumbs[min(1, len(in.Crumbs)):], ""),
		Total:   in.Total,
		Root:    strings.Repeat("../", depth),
		Archive: strings.Repeat("../", depth+1),
		Pager:   pager(in.Key.N, in.Total),
	}
	if in.Key.N > 1 {
		p.Prev = pageName(in.Key.N - 1)
	}
	if in.Key.N < in.Total {
		p.Next = pageName(in.Key.N + 1)
	}
	for _, m := range in.Messages {
		t := time.Unix(m.Date, 0).In(in.Loc)
		pm := PageMessage{
			ID:       m.ID,
			Time:     t.Format("15:04"),
			Sender:   orDefault(m.SenderName, "Deleted account"),
			HTML:     HTMLText(m.Text, m.EntitiesJSON),
			Markdown: MarkdownText(m.Text, m.EntitiesJSON),
			Edited:   m.EditDate != 0,
			Pinned:   m.Pinned,
			Forward:  forward(m, in.Loc),
			Service:  serviceText(m.ServiceActionJSON, in.Refs[m.ReplyToID].MetaJSON),
		}
		pm.Reactions = reactions(m.ReactionsJSON)
		for _, v := range in.Versions[m.ID] {
			when := "as sent " + t.Format("15:04")
			if v.EditDate != 0 {
				when = "edited " + time.Unix(v.EditDate, 0).In(in.Loc).Format("2006-01-02 15:04")
			}
			pv := PageVersion{When: when, Seen: "replaced by the next version by " + time.Unix(v.ReplacedAt, 0).In(in.Loc).Format("2006-01-02 15:04"),
				HTML: HTMLText(v.Text, v.EntitiesJSON), Markdown: MarkdownText(v.Text, v.EntitiesJSON)}
			if len(v.MediaKeys) > 0 && (m.Media == nil || !slices.Contains(v.MediaKeys, m.Media.Key)) {
				pv.Attachment = true
			}
			pm.Versions = append(pm.Versions, pv)
		}
		if m.ReplyToID != 0 {
			pm.Reply = reply(in, m.ReplyToID)
		}
		if m.Media != nil {
			pm.Media = media(m.Media, p.Archive)
		}
		if meta, ok := parseMeta(m.MetaJSON); ok {
			photo := ""
			// A link preview's picture is shown in its card, not as a separate photo.
			if pm.Media != nil && meta.WebPage != nil && meta.WebPage.PhotoKey == m.Media.Key {
				photo, pm.Media = pm.Media.Href, nil
			}
			pm.Meta = buildMeta(meta, photo, in.Names, in.Loc)
		}
		day := t.Format("2006-01-02")
		if len(p.Days) == 0 || p.Days[len(p.Days)-1].Date != day {
			p.Days = append(p.Days, Day{Date: day, Anchor: "d" + day})
		}
		d := &p.Days[len(p.Days)-1]
		d.Messages = append(d.Messages, pm)
	}
	if len(p.Days) > 0 {
		p.Range = p.Days[0].Date
		if last := p.Days[len(p.Days)-1].Date; last != p.Range {
			p.Range += " – " + last
		}
	}
	return p
}

// pager lists the first and last pages, the current one with two neighbours
// each side, and gaps between them.
func pager(n, total int) []PagerItem {
	var out []PagerItem
	last := 0
	for i := 1; i <= total; i++ {
		if i != 1 && i != total && (i < n-2 || i > n+2) {
			continue
		}
		if last != 0 && i > last+1 {
			out = append(out, PagerItem{Label: "…", Gap: true})
		}
		item := PagerItem{Label: strconv.Itoa(i), Href: pageName(i), Current: i == n}
		if item.Current {
			item.Href = ""
		}
		out = append(out, item)
		last = i
	}
	return out
}

func reply(in pageInput, id int) *Reply {
	ref, ok := in.Refs[id]
	if !ok {
		return &Reply{ID: id, Missing: true}
	}
	r := &Reply{ID: id, Sender: orDefault(ref.SenderName, "Deleted account"), Text: preview(ref.Text, ref.MediaKind)}
	target, ok := in.Locate(in.Key.ChatID, id)
	switch {
	case !ok:
		r.NoLink = true
	case target == in.Key:
	default:
		r.Page = relPage(in.Dir, in.DirOf(in.Key.ChatID, target.TopicID), pageName(target.N))
	}
	return r
}

// relPage is the target page relative to dir, URL-escaped, without extension.
func relPage(dir, targetDir, name string) string {
	from, to := strings.Split(dir, "/"), strings.Split(targetDir, "/")
	common := 0
	for common < len(from) && common < len(to) && from[common] == to[common] {
		common++
	}
	parts := make([]string, 0, len(from)-common+len(to)-common+1)
	for range from[common:] {
		parts = append(parts, "..")
	}
	for _, seg := range to[common:] {
		parts = append(parts, url.PathEscape(seg))
	}
	return strings.Join(append(parts, name), "/")
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func preview(text, mediaKind string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" && mediaKind != "" {
		return "[" + mediaKind + "]"
	}
	if utf8.RuneCountInString(text) > 100 {
		return string([]rune(text)[:100]) + "…"
	}
	return text
}

func forward(m store.RenderMessage, loc *time.Location) string {
	if m.FwdFromJSON == "" {
		return ""
	}
	var f struct {
		FromName   string `json:"from_name"`
		PostAuthor string `json:"post_author"`
		Date       int64  `json:"date"`
	}
	_ = json.Unmarshal([]byte(m.FwdFromJSON), &f)
	name := orDefault(m.FwdName, orDefault(f.FromName, "unknown"))
	if f.PostAuthor != "" {
		name += " (" + f.PostAuthor + ")"
	}
	if f.Date == 0 {
		return "Forwarded from " + name
	}
	return "Forwarded from " + name + " · " + time.Unix(f.Date, 0).In(loc).Format("2006-01-02")
}

var kindLabels = map[string]string{"video_note": "round video", "animation": "GIF"}

func media(m *store.RenderMedia, archive string) *PageMedia {
	label := orDefault(kindLabels[m.Kind], m.Kind)
	pm := &PageMedia{Kind: m.Kind, Name: orDefault(m.FileName, label), View: mediaView(m.Kind, m.Mime)}
	if m.Size > 0 {
		pm.Size = byteSize(m.Size)
	}
	switch {
	case m.Status == "done" && m.LocalPath != "":
		pm.Href = archive + escapePath(m.LocalPath)
	case m.Status == "skipped":
		pm.Note, pm.State = label+" not downloaded: "+orDefault(m.Error, "skipped"), "skipped"
	case m.Status == "failed":
		pm.Note, pm.State = label+" download failed: "+orDefault(m.Error, "unknown error"), "failed"
	default:
		pm.Note, pm.State = label+" not downloaded yet", "pending"
	}
	return pm
}

func mediaView(kind, mime string) string {
	switch kind {
	case "photo":
		return "img"
	case "sticker":
		switch {
		case strings.HasPrefix(mime, "video/"):
			return "loop"
		case strings.HasPrefix(mime, "image/"):
			return "img"
		}
		return "file" // an animated .tgs: a browser cannot draw it
	case "animation":
		if strings.HasPrefix(mime, "image/") {
			return "img" // a real .gif file
		}
		return "loop"
	case "video":
		return "video"
	case "video_note":
		return "round"
	case "voice", "audio":
		return kind
	}
	return "file"
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var serviceTexts = map[string]string{
	"messageActionPinMessage":          "pinned a message",
	"messageActionChatAddUser":         "joined",
	"messageActionChatJoinedByLink":    "joined by link",
	"messageActionChatJoinedByRequest": "joined",
	"messageActionChatDeleteUser":      "left",
	"messageActionChatEditPhoto":       "changed the photo",
	"messageActionChatDeletePhoto":     "removed the photo",
	"messageActionChatMigrateTo":       "upgraded to a supergroup",
	"messageActionChannelMigrateFrom":  "upgraded from a group",
	"messageActionTopicEdit":           "edited the topic",
	"messageActionPhoneCall":           "call",
	"messageActionHistoryClear":        "cleared history",
}

// serviceText says what a service message did; refMeta is the meta of the
// message it refers to (a checklist, for checklist events).
func serviceText(actionJSON, refMeta string) string {
	if actionJSON == "" {
		return ""
	}
	if t := convert.TodoEventText(actionJSON, refMeta); t != "" {
		return t
	}
	var a struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal([]byte(actionJSON), &a)
	title, _ := a.Data["Title"].(string)
	switch a.Type {
	case "messageActionTopicCreate":
		return "created topic «" + title + "»"
	case "messageActionChatEditTitle":
		return "changed the title to «" + title + "»"
	case "messageActionChatCreate", "messageActionChannelCreate":
		return "created the group «" + title + "»"
	}
	if s, ok := serviceTexts[a.Type]; ok {
		return s
	}
	return orDefault(strings.TrimPrefix(a.Type, "messageAction"), "service message")
}

func reactions(reactionsJSON string) string {
	if reactionsJSON == "" {
		return ""
	}
	var rs []struct {
		Emoji  string `json:"emoji"`
		Custom int64  `json:"custom_emoji_id"`
		Paid   bool   `json:"paid"`
		Count  int    `json:"count"`
	}
	_ = json.Unmarshal([]byte(reactionsJSON), &rs)
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		icon := r.Emoji
		switch {
		case r.Paid:
			icon = "⭐"
		case r.Custom != 0 || icon == "":
			icon = "★"
		}
		parts = append(parts, fmt.Sprintf("%s %d", icon, r.Count))
	}
	return strings.Join(parts, " · ")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
