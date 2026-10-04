// Package render turns the archive into static HTML pages and Markdown notes:
// every chat (or forum topic) thread is cut into pages of a fixed number of
// messages, with a date calendar, chat and archive indexes and a search
// index. Pages whose content did not change are not redrawn.
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

	"gitlab.com/joeunderwood/tgbup/internal/names"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// DefaultPageSize is how many messages one page holds.
const DefaultPageSize = 500

// layoutVersion changes when the files render writes change shape; an archive
// drawn with another version is cleared and drawn again.
const layoutVersion = "pages-1"

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
	PageSize int
	Loc      *time.Location
	// Now stamps "archive as of"; nil is the clock.
	Now func() time.Time
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

// thread is one chat's or topic's run of pages.
type thread struct {
	ChatID  int64
	TopicID int
	Dir     string
	Pages   []store.ThreadPage
}

func Render(ctx context.Context, db *store.Store, opts Options) (Result, error) {
	var res Result
	if opts.PageSize <= 0 {
		opts.PageSize = DefaultPageSize
	}
	if opts.Track {
		if err := upgradeLayout(ctx, db, opts.OutDir); err != nil {
			return res, err
		}
	}
	chats, err := db.RenderChats(ctx)
	if err != nil {
		return res, err
	}
	l := newLayout(chats)
	q := store.PageQuery{ChatIDs: opts.ChatIDs, Topics: opts.Topics, Size: opts.PageSize}
	if q.From, q.To, err = monthRange(opts.From, opts.To, opts.Loc); err != nil {
		return res, err
	}
	pages, err := db.Pages(ctx, q)
	if err != nil {
		return res, err
	}
	done := map[store.PageKey]string{}
	if opts.Track && !opts.Full {
		if done, err = db.RenderedFingerprints(ctx); err != nil {
			return res, err
		}
	}
	where := map[int64]map[int]store.PageKey{}
	for _, p := range pages {
		if where[p.ChatID] == nil {
			where[p.ChatID] = map[int]store.PageKey{}
		}
		for _, id := range p.IDs {
			where[p.ChatID][id] = p.PageKey
		}
	}
	locate := func(chat int64, id int) (store.PageKey, bool) {
		k, ok := where[chat][id]
		return k, ok
	}

	changed := map[int64]bool{}
	for _, p := range pages {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		dir := l.dir(p.ChatID, p.TopicID)
		// What a page shows also depends on where it lives, how many pages its
		// thread has (the pager), the time zone and the templates, so those redraw it too.
		fp := strings.Join([]string{p.Fingerprint, dir, strconv.Itoa(p.Total), zoneAt(p.First, opts.Loc), templatesHash}, "|")
		// The files are checked too: html/ and md/ may be deleted, or missing
		// after a restore, while the fingerprints come back with the database.
		if done[p.PageKey] == fp && pageFilesExist(opts.OutDir, dir, p.N) {
			res.Skipped++
			continue
		}
		media, err := renderPage(ctx, db, l, opts, p, locate)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("page %s/%s: %w", dir, pageName(p.N), err))
			// Forget the old fingerprint: the file may be gone, so retry next time.
			if opts.Track {
				if err := db.SetRendered(ctx, p.PageKey, ""); err != nil {
					return res, err
				}
			}
			continue
		}
		res.Pages++
		res.Messages += len(p.IDs)
		res.Media = append(res.Media, media...)
		changed[p.ChatID] = true
		if opts.Track {
			if err := db.SetRendered(ctx, p.PageKey, fp); err != nil {
				return res, err
			}
		}
	}
	if !opts.Track {
		// An export counts everything it contains; a tracked render counts what it redrew.
		res.Messages = 0
		for _, p := range pages {
			res.Messages += len(p.IDs)
		}
	}
	threads := groupThreads(l, pages)
	if err := writeThreadIndexes(l, opts, threads); err != nil {
		return res, err
	}
	if err := writeIndexes(ctx, db, l, opts, threads, where, changed); err != nil {
		return res, err
	}
	return res, nil
}

// upgradeLayout clears html/ and md/ drawn by an older layout, so no stale
// pages are left behind, and makes every page draw again.
func upgradeLayout(ctx context.Context, db *store.Store, outDir string) error {
	v, _, err := db.Meta(ctx, "render_layout")
	if err != nil || v == layoutVersion {
		return err
	}
	for _, d := range []string{"html", "md"} {
		if err := os.RemoveAll(filepath.Join(outDir, d)); err != nil {
			return err
		}
	}
	if err := db.ClearRendered(ctx); err != nil {
		return err
	}
	return db.SetMeta(ctx, "render_layout", layoutVersion)
}

func monthRange(from, to string, loc *time.Location) (int64, int64, error) {
	var a, b int64
	if from != "" {
		t, err := time.ParseInLocation("2006-01", from, loc)
		if err != nil {
			return 0, 0, err
		}
		a = t.Unix()
	}
	if to != "" {
		t, err := time.ParseInLocation("2006-01", to, loc)
		if err != nil {
			return 0, 0, err
		}
		b = t.AddDate(0, 1, 0).Unix()
	}
	return a, b, nil
}

func zoneAt(unix int64, loc *time.Location) string {
	name, offset := time.Unix(unix, 0).In(loc).Zone()
	return name + strconv.Itoa(offset)
}

func renderPage(ctx context.Context, db *store.Store, l *layout, opts Options, p store.ThreadPage,
	locate func(int64, int) (store.PageKey, bool)) ([]string, error) {
	msgs, err := db.PageMessages(ctx, p.ChatID, p.IDs)
	if err != nil {
		return nil, err
	}
	var replyIDs []int
	var userIDs []int64
	var media []string
	for _, msg := range msgs {
		if msg.ReplyToID != 0 {
			replyIDs = append(replyIDs, msg.ReplyToID)
		}
		userIDs = append(userIDs, todoUsers(msg.MetaJSON)...)
		if msg.Media != nil && msg.Media.Status == "done" && msg.Media.LocalPath != "" {
			media = append(media, msg.Media.LocalPath)
		}
	}
	refs, err := db.MessageRefs(ctx, p.ChatID, replyIDs)
	if err != nil {
		return nil, err
	}
	names, err := db.Names(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	dir := l.dir(p.ChatID, p.TopicID)
	page := buildPage(pageInput{
		Key: p.PageKey, Total: p.Total, Dir: dir, Messages: msgs, Refs: refs, DirOf: l.dir, Locate: locate,
		Names: names, Loc: opts.Loc, Crumbs: l.crumbs(p.ChatID, p.TopicID),
	})
	if err := writeFile(filepath.Join(opts.OutDir, "html", filepath.FromSlash(dir), pageName(p.N)+".html"), func(w io.Writer) error {
		return writePageHTML(w, page)
	}); err != nil {
		return nil, err
	}
	err = writeFile(filepath.Join(opts.OutDir, "md", filepath.FromSlash(dir), pageName(p.N)+".md"), func(w io.Writer) error {
		return writePageMD(w, page)
	})
	return media, err
}

func groupThreads(l *layout, pages []store.ThreadPage) []*thread {
	var out []*thread
	for _, p := range pages {
		if n := len(out); n == 0 || out[n-1].ChatID != p.ChatID || out[n-1].TopicID != p.TopicID {
			out = append(out, &thread{ChatID: p.ChatID, TopicID: p.TopicID, Dir: l.dir(p.ChatID, p.TopicID)})
		}
		t := out[len(out)-1]
		t.Pages = append(t.Pages, p)
	}
	return out
}

func (t *thread) messages() int {
	n := 0
	for _, p := range t.Pages {
		n += len(p.IDs)
	}
	return n
}

func (t *thread) latest() string { return pageName(len(t.Pages)) }

func dateRange(first, last int64, loc *time.Location) string {
	a, b := time.Unix(first, 0).In(loc).Format("2006-01-02"), time.Unix(last, 0).In(loc).Format("2006-01-02")
	if a == b {
		return a
	}
	return a + " – " + b
}

func (t *thread) dateRange(loc *time.Location) string {
	return dateRange(t.Pages[0].First, t.Pages[len(t.Pages)-1].Last, loc)
}

type calendar struct {
	ChatID int64
	Title  string
	Crumbs []string
	Root   string
	Latest string
	Years  []calYear
	Pages  []calPage
}

type calYear struct {
	Year   string
	Months []calMonth
}

type calMonth struct {
	Name string
	Days []calDay
}

type calDay struct {
	Day    string
	Href   string // to the day's anchor on its page, .html
	MDHref string
	Count  int
}

type calPage struct {
	N     int
	Href  string
	Range string
	Count int
}

// writeNav writes nav.js: the thread's pages and which page holds each day,
// for the page and date pickers on every page of the thread. Only dates and
// file names: nothing a sender wrote.
func writeNav(w io.Writer, cal calendar) error {
	type navPage struct {
		N     int    `json:"n"`
		Href  string `json:"href"`
		Range string `json:"range"`
	}
	nav := struct {
		Pages []navPage         `json:"pages"`
		Days  map[string]string `json:"days"`
	}{Days: map[string]string{}}
	for _, p := range cal.Pages {
		nav.Pages = append(nav.Pages, navPage{N: p.N, Href: p.Href + ".html", Range: p.Range})
	}
	for _, y := range cal.Years {
		for _, m := range y.Months {
			for _, d := range m.Days {
				if _, date, ok := strings.Cut(d.Href, "#d"); ok {
					nav.Days[date] = d.Href
				}
			}
		}
	}
	b, err := json.Marshal(nav)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "window.tgbupNav = %s;\n", b)
	return err
}

// writeThreadIndexes writes each thread's calendar (dates.html, dates.md).
func writeThreadIndexes(l *layout, opts Options, threads []*thread) error {
	for _, t := range threads {
		crumbs := l.crumbs(t.ChatID, t.TopicID)
		cal := calendar{ChatID: t.ChatID, Title: strings.Join(crumbs, " › "), Crumbs: crumbs,
			Root: strings.Repeat("../", strings.Count(t.Dir, "/")+1), Latest: t.latest()}
		for _, p := range t.Pages {
			cal.Pages = append(cal.Pages, calPage{N: p.N, Href: pageName(p.N), Range: dateRange(p.First, p.Last, opts.Loc), Count: len(p.IDs)})
			for _, d := range p.Dates {
				day := time.Unix(d, 0).In(opts.Loc)
				year, month := day.Format("2006"), day.Format("January 2006")
				if n := len(cal.Years); n == 0 || cal.Years[n-1].Year != year {
					cal.Years = append(cal.Years, calYear{Year: year})
				}
				y := &cal.Years[len(cal.Years)-1]
				if n := len(y.Months); n == 0 || y.Months[n-1].Name != month {
					y.Months = append(y.Months, calMonth{Name: month})
				}
				m := &y.Months[len(y.Months)-1]
				label := strconv.Itoa(day.Day())
				if n := len(m.Days); n > 0 && m.Days[n-1].Day == label {
					m.Days[n-1].Count++
					continue
				}
				date := day.Format("2006-01-02")
				m.Days = append(m.Days, calDay{Day: label, Count: 1,
					Href: pageName(p.N) + ".html#d" + date, MDHref: pageName(p.N) + ".md#" + date})
			}
		}
		dir := filepath.FromSlash(t.Dir)
		if err := writeFile(filepath.Join(opts.OutDir, "html", dir, "dates.html"), func(w io.Writer) error {
			return datesHTML.Execute(w, cal)
		}); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(opts.OutDir, "html", dir, "nav.js"), func(w io.Writer) error {
			return writeNav(w, cal)
		}); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(opts.OutDir, "md", dir, "dates.md"), func(w io.Writer) error {
			return datesMD.Execute(w, cal)
		}); err != nil {
			return err
		}
		// A topic folder opens on its latest page; a chat folder gets its own index.
		if strings.Contains(t.Dir, "/") {
			if err := writeFile(filepath.Join(opts.OutDir, "html", dir, "index.html"), func(w io.Writer) error {
				return redirectHTML.Execute(w, t.latest()+".html")
			}); err != nil {
				return err
			}
		}
	}
	return nil
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
	fcs := make([]names.FolderChat, 0, len(chats))
	for _, c := range chats {
		fc := names.FolderChat{ID: c.ID, Title: c.Title}
		if up, ok := l.upgradedTo[c.ID]; ok {
			fc.UpgradedTo, fc.UpgradedTitle = up, l.chats[up].Title
		}
		fcs = append(fcs, fc)
	}
	l.folder = names.FolderNames(fcs)
	for _, c := range chats {
		if c.Type != store.ChatForum {
			continue
		}
		topics := map[int]string{}
		seen := map[string]int{}
		for id, title := range c.Topics {
			name := names.Clean(title)
			if name == "" || name == beforeUpgrade {
				name = "Topic " + strconv.Itoa(id)
			}
			topics[id] = name
			seen[strings.ToLower(name)]++
		}
		for id, name := range topics {
			if seen[strings.ToLower(name)] > 1 {
				topics[id] = fmt.Sprintf("%s #%d", name, id)
			}
		}
		l.topics[c.ID] = topics
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
	Forum    bool
	Href     string // chat index relative to html/ or md/ root, without extension
	Messages int
	Last     string
	last     int64
	Threads  []indexThread
}

type indexThread struct {
	Title    string
	Latest   string // relative to the chat folder, without extension
	Dates    string // .html
	DatesMD  string
	Messages int
	Range    string
	last     int64
}

type siteIndex struct {
	Chats       []indexChat
	SearchFiles []string
}

// updatedJS fills every ".asof" element with when the archive was drawn.
const updatedJS = `window.tgbupUpdated = %q;
document.addEventListener('DOMContentLoaded', () => {
  const when = new Date(window.tgbupUpdated).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  for (const s of document.querySelectorAll('.asof')) s.textContent = '· archive as of ' + when;
});
`

func writeIndexes(ctx context.Context, db *store.Store, l *layout, opts Options, threads []*thread,
	where map[int64]map[int]store.PageKey, changed map[int64]bool) error {
	byOwner := map[int64]*indexChat{}
	var order []int64
	for _, t := range threads {
		owner := l.owner(t.ChatID)
		ic, ok := byOwner[owner]
		if !ok {
			c := l.chats[owner]
			ic = &indexChat{ID: owner, Title: orDefault(c.Title, strconv.FormatInt(owner, 10)), Type: c.Type,
				Forum: c.Type == store.ChatForum, Href: escapePath(l.folder[owner]) + "/index"}
			byOwner[owner] = ic
			order = append(order, owner)
		}
		crumbs := l.crumbs(t.ChatID, t.TopicID)
		sub := ""
		title := ""
		if len(crumbs) > 1 {
			title = crumbs[1]
			sub = escapePath(crumbs[1]) + "/"
		}
		last := t.Pages[len(t.Pages)-1].Last
		ic.Threads = append(ic.Threads, indexThread{Title: title, Latest: sub + t.latest(), Dates: sub + "dates.html",
			DatesMD: sub + "dates.md", Messages: t.messages(), Range: t.dateRange(opts.Loc), last: last})
		ic.Messages += t.messages()
		ic.last = max(ic.last, last)
	}
	chats := make([]indexChat, 0, len(order))
	for _, id := range order {
		ic := byOwner[id]
		ic.Last = time.Unix(ic.last, 0).In(opts.Loc).Format("2006-01-02")
		// Recently active topics first; "before upgrade" (the oldest history) last.
		slices.SortFunc(ic.Threads, func(a, b indexThread) int {
			if (a.Title == beforeUpgrade) != (b.Title == beforeUpgrade) {
				if a.Title == beforeUpgrade {
					return 1
				}
				return -1
			}
			return cmp.Or(cmp.Compare(b.last, a.last), cmp.Compare(a.Title, b.Title))
		})
		chats = append(chats, *ic)
	}
	slices.SortFunc(chats, func(a, b indexChat) int { return cmp.Or(cmp.Compare(b.last, a.last), cmp.Compare(a.Title, b.Title)) })

	var searchIDs []int64
	for id := range where {
		searchIDs = append(searchIDs, id)
	}
	slices.Sort(searchIDs)
	for _, id := range searchIDs {
		path := filepath.Join(opts.OutDir, "html", "search", strconv.FormatInt(id, 10)+".js")
		if _, err := os.Stat(path); err == nil && !changed[id] && !opts.Full {
			continue
		}
		if err := writeSearch(ctx, db, l, opts, id, where[id], path); err != nil {
			return err
		}
	}
	for _, id := range searchIDs {
		path := filepath.Join(opts.OutDir, "html", "media", strconv.FormatInt(id, 10)+".js")
		if _, err := os.Stat(path); err == nil && !changed[id] && !opts.Full {
			continue
		}
		if err := writeMediaIndex(ctx, db, l, opts, id, where[id], path); err != nil {
			return err
		}
	}
	if err := writeMediaPage(l, opts, chats, searchIDs); err != nil {
		return err
	}
	// Every page shows when the archive was last drawn from one small file,
	// so a page that did not change need not be drawn again for it.
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if err := writeFile(filepath.Join(opts.OutDir, "html", "assets", "updated.js"), func(w io.Writer) error {
		_, err := fmt.Fprintf(w, updatedJS, now().UTC().Format(time.RFC3339))
		return err
	}); err != nil {
		return err
	}
	for _, a := range []string{"style.css", "page.js", "search.js", "media.js", "local.js"} {
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
	idx := siteIndex{Chats: chats}
	for _, id := range searchIDs {
		idx.SearchFiles = append(idx.SearchFiles, "search/"+strconv.FormatInt(id, 10)+".js")
	}
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
		write := func(w io.Writer) error { return chatHTML.Execute(w, c) }
		// A chat with one thread at its folder root opens straight on the latest page.
		if len(c.Threads) == 1 && c.Threads[0].Title == "" {
			write = func(w io.Writer) error { return redirectHTML.Execute(w, c.Threads[0].Latest+".html") }
		}
		if err := writeFile(filepath.Join(opts.OutDir, "html", dir, "index.html"), write); err != nil {
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

type mediaChat struct {
	ID    int64
	Title string
}

// writeMediaPage writes media.html, which filters every chat's media index.
func writeMediaPage(l *layout, opts Options, chats []indexChat, ids []int64) error {
	page := struct {
		Chats []mediaChat
		Files []string
	}{}
	for _, c := range chats {
		page.Chats = append(page.Chats, mediaChat{ID: c.ID, Title: c.Title})
	}
	for _, id := range ids {
		page.Files = append(page.Files, "media/"+strconv.FormatInt(id, 10)+".js")
		// The group a chat was upgraded from is filtered with that chat.
		if up, ok := l.upgradedTo[id]; ok && slices.ContainsFunc(chats, func(c indexChat) bool { return c.ID == up }) {
			page.Chats = append(page.Chats, mediaChat{ID: id, Title: strings.Join(l.crumbs(id, 0), " › ")})
		}
	}
	return writeFile(filepath.Join(opts.OutDir, "html", "media.html"), func(w io.Writer) error {
		return mediaHTML.Execute(w, page)
	})
}

// writeSearch writes a chat's search index as a script: pages opened from
// disk (file://) may not fetch JSON, but may load scripts.
func writeSearch(ctx context.Context, db *store.Store, l *layout, opts Options, chat int64, where map[int]store.PageKey, path string) error {
	texts, err := db.SearchTexts(ctx, chat)
	if err != nil {
		return err
	}
	entries := make([][4]any, 0, len(texts))
	for _, t := range texts {
		k, ok := where[t.ID]
		if !ok {
			continue
		}
		page := escapePath(l.dir(chat, t.TopicID)) + "/" + pageName(k.N) + ".html#m" + strconv.Itoa(t.ID)
		text := strings.TrimSpace(t.Text + " " + metaText(t.MetaJSON))
		entries = append(entries, [4]any{t.ID, time.Unix(t.Date, 0).In(opts.Loc).Format("2006-01-02 15:04"), page, text})
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

func pageFilesExist(out, dir string, n int) bool {
	for _, f := range []string{filepath.Join(out, "html", filepath.FromSlash(dir), pageName(n)+".html"),
		filepath.Join(out, "md", filepath.FromSlash(dir), pageName(n)+".md")} {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}
	return true
}
