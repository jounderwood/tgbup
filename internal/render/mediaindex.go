package render

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jounderwood/tgbup/internal/store"
)

// mediaEntry is one item on the media page. Keys are short: a big chat has
// tens of thousands of them.
type mediaEntry struct {
	K string `json:"k"`           // kind: photo, video, round, gif, sticker, voice, audio, file, link, place, contact, poll, todo
	D string `json:"d"`           // date and time, "2006-01-02 15:04"
	T string `json:"t,omitempty"` // topic or thread part
	M string `json:"m"`           // the message, relative to html/
	F string `json:"f,omitempty"` // the file (relative to html/) or the outside link
	N string `json:"n,omitempty"` // name or title
	S string `json:"s,omitempty"` // size
	X string `json:"x,omitempty"` // why a file is not there
	V string `json:"v,omitempty"` // how a file is shown, PageMedia.View
}

var mediaKinds = map[string]string{
	"photo": "photo", "video": "video", "video_note": "round", "animation": "gif", "sticker": "sticker",
	"voice": "voice", "audio": "audio", "document": "file",
}

var voiceLabels = map[string]string{"voice": "Voice message", "video_note": "Round video"}

func mediaEntries(it store.MediaItem, msgHref, thread string, loc *time.Location) []mediaEntry {
	base := mediaEntry{D: time.Unix(it.Date, 0).In(loc).Format("2006-01-02 15:04"), T: thread, M: msgHref}
	var out []mediaEntry
	meta, hasMeta := parseMeta(it.MetaJSON)
	seen := map[string]bool{}
	if m := it.Media; m != nil {
		pm := media(m, "../")
		e := base
		e.K, e.F, e.N, e.S, e.X = orDefault(mediaKinds[m.Kind], "file"), pm.Href, pm.Name, pm.Size, pm.Note
		e.V = pm.View
		if m.Kind == "voice" || m.Kind == "video_note" {
			// They have no name of their own: say whose they are.
			e.N = orDefault(voiceLabels[m.Kind], m.Kind)
			if it.Sender != "" {
				e.N += " from " + it.Sender
			}
		}
		if hasMeta && meta.WebPage != nil && meta.WebPage.PhotoKey == m.Key {
			e = mediaEntry{} // a link preview's picture: the link entry below stands for it
		}
		if e.K != "" {
			out = append(out, e)
		}
	}
	if hasMeta {
		if w := meta.WebPage; w != nil && w.URL != "" {
			if href, ok := safeURL(w.URL); ok {
				e := base
				e.K, e.F, e.N = "link", href, orDefault(w.Title, w.URL)
				out = append(out, e)
				seen[href] = true
			}
		}
		if p := buildMeta(meta, "", nil, loc); p != nil {
			if pl := p.Place; pl != nil {
				e := base
				e.K, e.F, e.N = "place", pl.MapURL, orDefault(pl.Name, pl.Coords)
				out = append(out, e)
			}
			if c := p.Contact; c != nil {
				e := base
				e.K, e.N = "contact", strings.TrimSpace(c.Name+" "+c.Phone)
				out = append(out, e)
			}
			if q := p.Poll; q != nil {
				e := base
				e.K, e.N = "poll", q.Question
				out = append(out, e)
			}
			if td := p.Todo; td != nil {
				e := base
				e.K, e.N = "todo", td.Title
				out = append(out, e)
			}
		}
	}
	for _, href := range textLinks(it.Text, it.EntitiesJSON) {
		if !seen[href] {
			seen[href] = true
			e := base
			e.K, e.F, e.N = "link", href, href
			out = append(out, e)
		}
	}
	return out
}

// textLinks lists the safe links written in a message's text.
func textLinks(text, entitiesJSON string) []string {
	var ents []entity
	if entitiesJSON == "" || json.Unmarshal([]byte(entitiesJSON), &ents) != nil {
		return nil
	}
	units := utf16.Encode([]rune(text))
	var out []string
	for _, e := range ents {
		if e.Type != "messageEntityUrl" && e.Type != "messageEntityTextUrl" {
			continue
		}
		start := min(max(e.Offset, 0), len(units))
		end := min(max(e.Offset+e.Length, start), len(units))
		if href, ok := entityHref(&span{entity: e, start: start, end: end, text: string(utf16.Decode(units[start:end]))}); ok {
			out = append(out, href)
		}
	}
	return out
}

// writeMediaIndex writes a chat's media page data as a script, like search.
func writeMediaIndex(ctx context.Context, db *store.Store, l *layout, opts Options, chat int64, where map[int]store.PageKey, path string) error {
	items, err := db.MediaItems(ctx, chat)
	if err != nil {
		return err
	}
	entries := make([]mediaEntry, 0, len(items))
	for _, it := range items {
		k, ok := where[it.ID]
		if !ok {
			continue
		}
		crumbs := l.crumbs(chat, it.TopicID)
		thread := ""
		if len(crumbs) > 1 {
			thread = crumbs[1]
		}
		msg := escapePath(l.dir(chat, it.TopicID)) + "/" + pageName(k.N) + ".html#m" + strconv.Itoa(it.ID)
		entries = append(entries, mediaEntries(it, msg, thread, opts.Loc)...)
	}
	data, err := json.Marshal(map[string]any{"id": chat, "items": entries})
	if err != nil {
		return err
	}
	return writeFile(path, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "tgbupMedia.add(%s);\n", data)
		return err
	})
}
