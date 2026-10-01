// Package render turns the archive into static HTML pages and Markdown notes:
// one page per chat (or forum topic) and month, chat and archive indexes, and
// a search index. Pages whose content did not change are not redrawn.
package render

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/browse"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

type Options struct {
	// OutDir receives html/ and md/; pages link media/ as its sibling.
	OutDir string
	// Track records page fingerprints so the next render skips unchanged
	// pages; an export renders elsewhere and must not touch them.
	Track bool
	Full  bool
	// ChatIDs, Topics and From/To ("2006-01", inclusive) narrow the render, for exports.
	ChatIDs  []int64
	Topics   []int
	From, To string
	Loc      *time.Location
}

type Result struct {
	Pages   int
	Skipped int
	Errors  []error
	// Messages and Media count what the rendered pages show (for export summaries).
	Messages int
	Media    []string
}

const beforeUpgrade = "before upgrade"

func Render(ctx context.Context, db *store.Store, opts Options) (Result, error) {
	var res Result
	chats, err := db.RenderChats(ctx)
	if err != nil {
		return res, err
	}
	l := newLayout(chats)
	months, err := db.Months(ctx, opts.ChatIDs, opts.Loc)
	if err != nil {
		return res, err
	}
	months = slices.DeleteFunc(months, func(m store.MonthKey) bool { return !opts.wants(m) })
	done := map[store.PageKey]string{}
	if opts.Track && !opts.Full {
		if done, err = db.RenderedFingerprints(ctx); err != nil {
			return res, err
		}
	}
	var inScope func(store.PageKey) bool
	if len(opts.Topics) > 0 || opts.From != "" || opts.To != "" {
		scope := map[store.PageKey]bool{}
		for _, m := range months {
			scope[m.Page()] = true
		}
		inScope = func(k store.PageKey) bool { return scope[k] }
	}

	changed := map[int64]bool{}
	for i, m := range months {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if done[m.Page()] == m.Fingerprint {
			res.Skipped++
			continue
		}
		var prev, next string
		if i > 0 && sameThread(months[i-1], m) {
			prev = months[i-1].Month
		}
		if i+1 < len(months) && sameThread(months[i+1], m) {
			next = months[i+1].Month
		}
		n, media, err := renderMonth(ctx, db, l, opts, m, prev, next, inScope)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("page %s/%s: %w", l.dir(m.ChatID, m.TopicID), m.Month, err))
			continue
		}
		res.Pages++
		res.Messages += n
		res.Media = append(res.Media, media...)
		changed[m.ChatID] = true
		if opts.Track {
			if err := db.SetRendered(ctx, m); err != nil {
				return res, err
			}
		}
	}
	if err := writeIndexes(ctx, db, l, opts, months, changed); err != nil {
		return res, err
	}
	return res, nil
}

func (o Options) wants(m store.MonthKey) bool {
	if len(o.Topics) > 0 && !slices.Contains(o.Topics, m.TopicID) {
		return false
	}
	return (o.From == "" || m.Month >= o.From) && (o.To == "" || m.Month <= o.To)
}

func sameThread(a, b store.MonthKey) bool { return a.ChatID == b.ChatID && a.TopicID == b.TopicID }

func renderMonth(ctx context.Context, db *store.Store, l *layout, opts Options, m store.MonthKey, prev, next string,
	inScope func(store.PageKey) bool) (int, []string, error) {
	msgs, err := db.MonthMessages(ctx, m.ChatID, m.TopicID, m.Month, opts.Loc)
	if err != nil {
		return 0, nil, err
	}
	var replyIDs []int
	var media []string
	for _, msg := range msgs {
		if msg.ReplyToID != 0 {
			replyIDs = append(replyIDs, msg.ReplyToID)
		}
		if msg.Media != nil && msg.Media.Status == "done" && msg.Media.LocalPath != "" {
			media = append(media, msg.Media.LocalPath)
		}
	}
	refs, err := db.MessageRefs(ctx, m.ChatID, replyIDs, opts.Loc)
	if err != nil {
		return 0, nil, err
	}
	dir := l.dir(m.ChatID, m.TopicID)
	page := buildPage(pageInput{
		Key: m.Page(), Dir: dir, Messages: msgs, Refs: refs, DirOf: l.dir, InScope: inScope,
		Loc: opts.Loc, Crumbs: l.crumbs(m.ChatID, m.TopicID), Prev: prev, Next: next,
	})
	if err := writeFile(filepath.Join(opts.OutDir, "html", filepath.FromSlash(dir), m.Month+".html"), func(w io.Writer) error {
		return writeMonthHTML(w, page)
	}); err != nil {
		return 0, nil, err
	}
	err = writeFile(filepath.Join(opts.OutDir, "md", filepath.FromSlash(dir), m.Month+".md"), func(w io.Writer) error {
		return writeMonthMD(w, page)
	})
	return len(msgs), media, err
}

// layout decides where every chat's and topic's pages live.
type layout struct {
	chats      map[int64]store.RenderChat
	folder     map[int64]string         // per owning chat
	upgradedTo map[int64]int64          // basic group → the supergroup it became
	topics     map[int64]map[int]string // forum → topic → folder
}

func newLayout(chats []store.RenderChat) *layout {
	l := &layout{chats: map[int64]store.RenderChat{}, upgradedTo: map[int64]int64{}, topics: map[int64]map[int]string{}}
	for _, c := range chats {
		l.chats[c.ID] = c
	}
	for _, c := range chats {
		if _, ok := l.chats[c.MigratedFromID]; ok && c.MigratedFromID != 0 {
			l.upgradedTo[c.MigratedFromID] = c.ID
		}
	}
	fcs := make([]browse.FolderChat, 0, len(chats))
	for _, c := range chats {
		fc := browse.FolderChat{ID: c.ID, Title: c.Title}
		if up, ok := l.upgradedTo[c.ID]; ok {
			fc.UpgradedTo, fc.UpgradedTitle = up, l.chats[up].Title
		}
		fcs = append(fcs, fc)
	}
	l.folder = browse.FolderNames(fcs)
	for _, c := range chats {
		if c.Type != store.ChatForum {
			continue
		}
		names := map[int]string{}
		seen := map[string]int{}
		for id, title := range c.Topics {
			name := browse.Clean(title)
			if name == "" || name == beforeUpgrade {
				name = "Topic " + strconv.Itoa(id)
			}
			names[id] = name
			seen[strings.ToLower(name)]++
		}
		for id, name := range names {
			if seen[strings.ToLower(name)] > 1 {
				names[id] = fmt.Sprintf("%s #%d", name, id)
			}
		}
		l.topics[c.ID] = names
	}
	return l
}

func (l *layout) owner(chat int64) int64 {
	if up, ok := l.upgradedTo[chat]; ok {
		return up
	}
	return chat
}

func (l *layout) topicName(chat int64, topic int) string {
	if name, ok := l.topics[chat][topic]; ok {
		return name
	}
	return "Topic " + strconv.Itoa(topic)
}

// dir is the slash-separated folder of a chat's (or topic's) pages inside html/ and md/.
func (l *layout) dir(chat int64, topic int) string {
	return strings.Join(l.crumbs(chat, topic), "/")
}

func (l *layout) crumbs(chat int64, topic int) []string {
	folder := l.folder[l.owner(chat)]
	switch {
	case l.owner(chat) != chat:
		return []string{folder, beforeUpgrade}
	case l.chats[chat].Type == store.ChatForum:
		return []string{folder, l.topicName(chat, topic)}
	}
	return []string{folder}
}

// writeFile writes through a temp file and a rename, so a page is never half-written.
func writeFile(path string, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type indexChat struct {
	ID       int64
	Title    string
	Type     string
	Href     string // chat index relative to html/ or md/ root, without extension
	Months   int
	Last     string
	Sections []indexSection
}

type indexSection struct {
	Title  string
	Last   string
	Months []indexMonth
}

type indexMonth struct {
	Month string
	Href  string // relative to the chat folder, without extension
}

func writeIndexes(ctx context.Context, db *store.Store, l *layout, opts Options, months []store.MonthKey, changed map[int64]bool) error {
	byOwner := map[int64]*indexChat{}
	var order []int64
	sections := map[int64]map[string]*indexSection{}
	searchChats := map[int64]bool{}
	for _, m := range months {
		owner := l.owner(m.ChatID)
		ic, ok := byOwner[owner]
		if !ok {
			c := l.chats[owner]
			ic = &indexChat{ID: owner, Title: orDefault(c.Title, strconv.FormatInt(owner, 10)), Type: c.Type,
				Href: escapePath(l.folder[owner]) + "/index"}
			byOwner[owner] = ic
			sections[owner] = map[string]*indexSection{}
			order = append(order, owner)
		}
		crumbs := l.crumbs(m.ChatID, m.TopicID)
		title := ""
		if len(crumbs) > 1 {
			title = crumbs[1]
		}
		sec, ok := sections[owner][title]
		if !ok {
			sec = &indexSection{Title: title}
			sections[owner][title] = sec
		}
		sub := strings.Join(crumbs[1:], "/")
		href := m.Month
		if sub != "" {
			href = escapePath(sub) + "/" + m.Month
		}
		sec.Months = append(sec.Months, indexMonth{Month: m.Month, Href: href})
		sec.Last = max(sec.Last, m.Month)
		ic.Months++
		ic.Last = max(ic.Last, m.Month)
		searchChats[m.ChatID] = true
	}
	chats := make([]indexChat, 0, len(order))
	for _, id := range order {
		ic := byOwner[id]
		for _, sec := range sections[id] {
			slices.Reverse(sec.Months) // newest first
			ic.Sections = append(ic.Sections, *sec)
		}
		// Topics with recent activity first; "before upgrade" (oldest history) last.
		slices.SortFunc(ic.Sections, func(a, b indexSection) int {
			if (a.Title == beforeUpgrade) != (b.Title == beforeUpgrade) {
				if a.Title == beforeUpgrade {
					return 1
				}
				return -1
			}
			return cmp.Or(cmp.Compare(b.Last, a.Last), cmp.Compare(a.Title, b.Title))
		})
		chats = append(chats, *ic)
	}
	slices.SortFunc(chats, func(a, b indexChat) int { return cmp.Or(cmp.Compare(b.Last, a.Last), cmp.Compare(a.Title, b.Title)) })

	var searchIDs []int64
	for id := range searchChats {
		searchIDs = append(searchIDs, id)
	}
	slices.Sort(searchIDs)
	for _, id := range searchIDs {
		path := filepath.Join(opts.OutDir, "html", "search", strconv.FormatInt(id, 10)+".js")
		if _, err := os.Stat(path); err == nil && !changed[id] && !opts.Full {
			continue
		}
		if err := writeSearch(ctx, db, l, opts, id, path); err != nil {
			return err
		}
	}
	for _, a := range []string{"style.css", "page.js", "search.js"} {
		body, err := files.ReadFile("assets/" + a)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(opts.OutDir, "html", "assets", a), func(w io.Writer) error {
			_, err := w.Write(body)
			return err
		}); err != nil {
			return err
		}
	}
	idx := siteIndex{Chats: chats, SearchIDs: searchIDs}
	if err := writeFile(filepath.Join(opts.OutDir, "html", "index.html"), func(w io.Writer) error {
		return indexHTML.Execute(w, idx)
	}); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(opts.OutDir, "md", "index.md"), func(w io.Writer) error {
		return indexMD.Execute(w, idx)
	}); err != nil {
		return err
	}
	for _, c := range chats {
		dir := filepath.FromSlash(l.folder[c.ID])
		if err := writeFile(filepath.Join(opts.OutDir, "html", dir, "index.html"), func(w io.Writer) error {
			return chatHTML.Execute(w, c)
		}); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(opts.OutDir, "md", dir, "index.md"), func(w io.Writer) error {
			return chatMD.Execute(w, c)
		}); err != nil {
			return err
		}
	}
	return nil
}

type siteIndex struct {
	Chats     []indexChat
	SearchIDs []int64
}

// writeSearch writes a chat's search index as a script: pages opened from
// disk (file://) may not fetch JSON, but may load scripts.
func writeSearch(ctx context.Context, db *store.Store, l *layout, opts Options, chat int64, path string) error {
	texts, err := db.SearchTexts(ctx, chat)
	if err != nil {
		return err
	}
	entries := make([][4]any, 0, len(texts))
	for _, t := range texts {
		when := time.Unix(t.Date, 0).In(opts.Loc)
		k := store.MonthKey{ChatID: chat, TopicID: t.TopicID, Month: when.Format("2006-01")}
		if !opts.wants(k) {
			continue
		}
		page := escapePath(l.dir(chat, t.TopicID)) + "/" + k.Month + ".html#m" + strconv.Itoa(t.ID)
		entries = append(entries, [4]any{t.ID, when.Format("2006-01-02 15:04"), page, t.Text})
	}
	crumbs := l.crumbs(chat, 0)
	if l.chats[chat].Type == store.ChatForum {
		crumbs = crumbs[:1]
	}
	data, err := json.Marshal(map[string]any{"chat": strings.Join(crumbs, " › "), "entries": entries})
	if err != nil {
		return err
	}
	return writeFile(path, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "tgbupSearch.add(%s);\n", data)
		return err
	})
}
