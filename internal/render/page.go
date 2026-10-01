package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// Page is one month of a chat or forum topic.
type Page struct {
	Title  string // "Family › Expenses · 2026-03"
	Crumbs []string
	Month  string
	// Root leads from the page to the html/ (or md/) root, Archive to the archive root.
	Root, Archive string
	Prev, Next    string // sibling month pages, without extension; "" when none
	Days          []Day
}

type Day struct {
	Date     string
	Messages []PageMessage
}

type PageMessage struct {
	ID        int
	Time      string
	Sender    string
	HTML      template.HTML
	Markdown  string
	Plain     string
	Edited    bool
	Pinned    bool
	Reply     *Reply
	Forward   string
	Media     *PageMedia
	Service   string
	Reactions string
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
}

type pageInput struct {
	Key      store.PageKey
	Dir      string // the page's folder inside html/ and md/, slash-separated
	Messages []store.RenderMessage
	Refs     map[int]store.MessageRef
	// DirOf gives the folder of another page of the same chat.
	DirOf func(chat int64, topic int) string
	// InScope reports whether a page exists in this render (an export may leave pages out).
	InScope    func(store.PageKey) bool
	Loc        *time.Location
	Crumbs     []string
	Prev, Next string
}

func buildPage(in pageInput) Page {
	depth := strings.Count(in.Dir, "/") + 1
	p := Page{
		Title:   strings.Join(in.Crumbs, " › ") + " · " + in.Key.Month,
		Crumbs:  in.Crumbs,
		Month:   in.Key.Month,
		Root:    strings.Repeat("../", depth),
		Archive: strings.Repeat("../", depth+1),
		Prev:    in.Prev,
		Next:    in.Next,
	}
	for _, m := range in.Messages {
		t := time.Unix(m.Date, 0).In(in.Loc)
		pm := PageMessage{
			ID:       m.ID,
			Time:     t.Format("15:04"),
			Sender:   orDefault(m.SenderName, "Deleted account"),
			HTML:     HTMLText(m.Text, m.EntitiesJSON),
			Markdown: MarkdownText(m.Text, m.EntitiesJSON),
			Plain:    m.Text,
			Edited:   m.EditDate != 0,
			Pinned:   m.Pinned,
			Forward:  forward(m, in.Loc),
			Service:  serviceText(m.ServiceActionJSON),
		}
		pm.Reactions = reactions(m.ReactionsJSON)
		if m.ReplyToID != 0 {
			pm.Reply = reply(in, m.ReplyToID)
		}
		if m.Media != nil {
			pm.Media = media(m.Media, p.Archive)
			if pm.Plain == "" {
				pm.Plain = pm.Media.Name
			}
		}
		day := t.Format("2006-01-02")
		if len(p.Days) == 0 || p.Days[len(p.Days)-1].Date != day {
			p.Days = append(p.Days, Day{Date: day})
		}
		d := &p.Days[len(p.Days)-1]
		d.Messages = append(d.Messages, pm)
	}
	return p
}

func reply(in pageInput, id int) *Reply {
	ref, ok := in.Refs[id]
	if !ok {
		return &Reply{ID: id, Missing: true}
	}
	r := &Reply{ID: id, Sender: orDefault(ref.SenderName, "Deleted account"), Text: preview(ref.Text, ref.MediaKind)}
	target := store.PageKey{ChatID: in.Key.ChatID, TopicID: ref.TopicID, Month: ref.Month}
	switch {
	case target == in.Key:
	case in.InScope != nil && !in.InScope(target):
		r.NoLink = true
	default:
		r.Page = relPage(in.Dir, in.DirOf(in.Key.ChatID, ref.TopicID), ref.Month)
	}
	return r
}

// relPage is the target page relative to dir, URL-escaped, without extension.
func relPage(dir, targetDir, month string) string {
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
	return strings.Join(append(parts, month), "/")
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
	pm := &PageMedia{Kind: m.Kind, Name: orDefault(m.FileName, label)}
	if m.Size > 0 {
		pm.Size = byteSize(m.Size)
	}
	switch {
	case m.Status == "done" && m.LocalPath != "":
		pm.Href = archive + escapePath(m.LocalPath)
	case m.Status == "skipped":
		pm.Note = label + " not downloaded: " + orDefault(m.Error, "skipped")
	case m.Status == "failed":
		pm.Note = label + " download failed: " + orDefault(m.Error, "unknown error")
	default:
		pm.Note = label + " not downloaded yet"
	}
	return pm
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

func serviceText(actionJSON string) string {
	if actionJSON == "" {
		return ""
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
