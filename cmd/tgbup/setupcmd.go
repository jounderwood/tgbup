package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"

	"github.com/jounderwood/tgbup/internal/config"
	"github.com/jounderwood/tgbup/internal/selector"
	"github.com/jounderwood/tgbup/internal/stats"
	"github.com/jounderwood/tgbup/internal/store"
	"github.com/jounderwood/tgbup/internal/tgclient"
)

const pageSize = 20

// rowInfo is what a chat list row shows next to the title.
type rowInfo struct {
	Msgs   int
	Topics int
	Err    error
}

type setupDialog struct {
	ctx      context.Context
	in       *lineReader
	out      io.Writer
	groups   []selector.Group
	dialogs  []tgclient.Dialog
	st       *selector.State
	cfgPath  string
	rowStats func([]tgclient.Dialog) map[int64]rowInfo
	details  func([]stats.Item) []stats.ChatStats
	// topicList lists a forum's topics with one request, uncounted.
	topicList func(tgclient.Dialog) ([]stats.TopicStats, error)
	// preview renders the config file that would be written.
	preview func(config.Selected) (string, error)

	rows map[int64]rowInfo
	// topics are the forums' topics seen so far; their titles go into config comments.
	topics map[int64][]stats.TopicStats
}

// lineReader reads input a line at a time, only when asked: a reader running
// ahead would take the lines, and the hidden 2FA password, meant for the next
// prompt or the Telegram login.
type lineReader struct {
	r     *bufio.Reader
	onErr func(error)
	// stopped is set once a read is abandoned; that read may still complete, so
	// no other read may start.
	stopped bool
}

func newLineReader(r io.Reader, onErr func(error)) *lineReader {
	return &lineReader{r: bufio.NewReader(r), onErr: onErr}
}

// next returns false at EOF, on a read error and when ctx ends (Ctrl+C).
func (l *lineReader) next(ctx context.Context) (string, bool) {
	if l.stopped || ctx.Err() != nil {
		return "", false
	}
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := l.r.ReadString('\n')
		ch <- result{s, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			if !errors.Is(res.err, io.EOF) && l.onErr != nil {
				l.onErr(res.err)
			}
			if res.s == "" || !errors.Is(res.err, io.EOF) {
				l.stopped = true
				return "", false
			}
		}
		return strings.TrimRight(res.s, "\r\n"), true
	case <-ctx.Done():
		l.stopped = true
		return "", false
	}
}

// buffered is the reader for the Telegram login: bufio.NewReader returns it
// as is, so nothing gets buffered twice.
func (l *lineReader) buffered() *bufio.Reader { return l.r }

// ask returns false on EOF (Ctrl+D) or interrupt (Ctrl+C cancels ctx), which
// always means "leave without saving".
func (s *setupDialog) ask(prompt string) (string, bool) {
	fmt.Fprint(s.out, prompt)
	line, ok := s.in.next(s.ctx)
	if !ok {
		fmt.Fprintln(s.out)
	}
	return strings.TrimSpace(line), ok
}

// russianLayout maps keys typed in the Russian layout to the commands on the
// same keys, so switching layout is not needed.
var russianLayout = map[string]string{
	"б": "<", "ю": ">", "ф": "a", "т": "n", "Ф": "A", "Т": "N", "е": "t", "з": "p", "ы": "s", "в": "d", "й": "q", "с": "c",
}

// normalizeCommand accepts Russian-layout commands and "p2"/"t3" without a space.
func normalizeCommand(line string) string {
	cmd, arg, _ := strings.Cut(line, " ")
	if en, ok := russianLayout[firstRune(cmd)]; ok && (len([]rune(cmd)) == 1 || isNumber(string([]rune(cmd)[1:]))) {
		cmd = en + string([]rune(cmd)[1:])
	}
	if len(cmd) > 1 && (cmd[0] == 'p' || cmd[0] == 't') && isNumber(cmd[1:]) {
		arg, cmd = cmd[1:], cmd[:1]
	}
	if arg == "" {
		return cmd
	}
	return cmd + " " + strings.TrimSpace(arg)
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func (s *setupDialog) run() bool {
	width := 0
	for _, g := range s.groups {
		width = max(width, displayWidth(g.Title))
	}
	for {
		fmt.Fprintf(s.out, "\nSelected: %s\n", plural(len(s.st.Chats), "chat"))
		for i, g := range s.groups {
			fmt.Fprintf(s.out, "  %d) %s  (%d, selected %d)\n", i+1, padRight(g.Title, width), len(g.Chats), s.st.Chats.Count(g.Chats))
		}
		fmt.Fprintln(s.out, "  s) search   d) done   q) quit without saving")
		line, ok := s.ask("> ")
		line = normalizeCommand(line)
		if !ok || line == "q" {
			return false
		}
		switch line {
		case "s":
			q, ok := s.ask("search: ")
			if !ok || !s.editList("Search: "+q, selector.Search(s.dialogs, q)) {
				return false
			}
		case "d":
			save, ok := s.confirm()
			if !ok {
				return false
			}
			if save {
				return true
			}
		default:
			n, err := strconv.Atoi(line)
			if err != nil || n < 1 || n > len(s.groups) {
				fmt.Fprintf(s.out, "unknown command %q\n", line)
				continue
			}
			if !s.editList(s.groups[n-1].Title, s.groups[n-1].Chats) {
				return false
			}
		}
	}
}

// editList shows chats a page at a time; numbers are global across pages so a
// range may span pages. It returns false on EOF or interrupt.
func (s *setupDialog) editList(title string, chats []tgclient.Dialog) bool {
	pages := max((len(chats)+pageSize-1)/pageSize, 1)
	page := 0
	for {
		from, to := page*pageSize, min((page+1)*pageSize, len(chats))
		s.printPage(title, chats, page, pages, chats[from:to])
		line, ok := s.ask("> ")
		if !ok {
			return false
		}
		line = normalizeCommand(line)
		cmd, arg, _ := strings.Cut(line, " ")
		switch {
		case line == "":
			return true
		case line == ">":
			if page == pages-1 {
				fmt.Fprintln(s.out, "already on the last page")
			} else {
				page++
			}
		case line == "<":
			if page == 0 {
				fmt.Fprintln(s.out, "already on the first page")
			} else {
				page--
			}
		case cmd == "p":
			n, err := strconv.Atoi(strings.TrimSpace(arg))
			if err != nil || n < 1 || n > pages {
				fmt.Fprintf(s.out, "error: no page %s (1-%d)\n", strings.TrimSpace(arg), pages)
				continue
			}
			page = n - 1
		case line == "a" || line == "n":
			s.st.Chats.SetAll(chats[from:to], line == "a")
		case line == "A" || line == "N":
			s.st.Chats.SetAll(chats, line == "A")
		case line == "c":
			s.loadRows(chats[from:to])
		case cmd == "t":
			n, err := strconv.Atoi(strings.TrimSpace(arg))
			if err != nil || n < 1 || n > len(chats) {
				fmt.Fprintf(s.out, "error: no chat %s (1-%d)\n", strings.TrimSpace(arg), len(chats))
				continue
			}
			if chats[n-1].Type != store.ChatForum {
				fmt.Fprintf(s.out, "%s is not a forum\n", cleanTitle(chats[n-1].Title, titleWidth))
				continue
			}
			if !s.editTopics(chats[n-1]) {
				return false
			}
		default:
			idx, err := selector.ParseIndices(line, len(chats))
			if err != nil {
				fmt.Fprintf(s.out, "error: %v\n", err)
				continue
			}
			s.st.Chats.Toggle(chats, idx)
		}
	}
}

func (s *setupDialog) printPage(title string, chats []tgclient.Dialog, page, pages int, visible []tgclient.Dialog) {
	fmt.Fprintf(s.out, "\n%s · page %d/%d · %s · %d selected\n", title, page+1, pages, plural(len(chats), "chat"), s.st.Chats.Count(chats))
	if len(chats) == 0 {
		fmt.Fprintln(s.out, "  (empty)")
		return
	}
	fmt.Fprintf(s.out, "  %4s  %-3s  %-*s  %-10s  %-8s  %s\n", "#", "sel", titleWidth, "chat", "type", "msgs", "topics")
	fmt.Fprintf(s.out, "  %s  %s  %s  %s  %s  %s\n", strings.Repeat("─", 4), strings.Repeat("─", 3),
		strings.Repeat("─", titleWidth), strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", 10))
	for i, c := range visible {
		mark := "[ ]"
		if s.st.Chats[c.ID] {
			mark = "[x]"
		}
		r, seen := s.rows[c.ID]
		msgs, topics := "~"+groupDigits(r.Msgs), ""
		switch {
		case !seen:
			msgs = "—" // counted on request: c
		case r.Err != nil:
			msgs = "?"
		case c.Type == store.ChatForum:
			topics = strconv.Itoa(r.Topics)
		}
		if n := len(s.st.ExcludedFor(c.ID)); n > 0 && c.Type == store.ChatForum {
			topics = strings.TrimSpace(topics + fmt.Sprintf(" (%d excl)", n))
		}
		line := fmt.Sprintf("  %4d  %s  %s  %-10s  %-8s  %s", page*pageSize+i+1, mark, padRight(cleanTitle(c.Title, titleWidth), titleWidth), c.Type, msgs, topics)
		fmt.Fprintln(s.out, strings.TrimRight(line, " "))
	}
	fmt.Fprintln(s.out, " 1,3,5-7 toggle · a/n page all/none · A/N whole list · t 3 topics · c count messages on this page")
	fmt.Fprintln(s.out, " > next · < prev · p 5 go to page · Enter back")
}

// loadRows fetches counts only for chats not seen yet, so paging back is instant.
func (s *setupDialog) loadRows(visible []tgclient.Dialog) {
	if s.rows == nil {
		s.rows = map[int64]rowInfo{}
	}
	var missing []tgclient.Dialog
	for _, c := range visible {
		if _, ok := s.rows[c.ID]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return
	}
	for id, r := range s.rowStats(missing) {
		s.rows[id] = r
	}
}

// editTopics returns false on EOF or interrupt.
func (s *setupDialog) editTopics(forum tgclient.Dialog) bool {
	topics, ok := s.topics[forum.ID]
	if !ok {
		var err error
		if topics, err = s.topicList(forum); err != nil {
			fmt.Fprintf(s.out, "could not load topics: %v\n", err)
			return true
		}
		s.keepTopics(forum.ID, topics)
	}
	if len(topics) == 0 {
		fmt.Fprintln(s.out, "no topics to choose from")
		return true
	}
	ids := make([]int, len(topics))
	for i, t := range topics {
		ids[i] = t.ID
	}
	cs := stats.ChatStats{Topics: topics}
	for {
		fmt.Fprintf(s.out, "\n%s · topics · new topics are archived automatically\n", cleanTitle(forum.Title, titleWidth))
		fmt.Fprintf(s.out, "  %4s  %-4s  %-*s  %s\n", "#", "arch", titleWidth, "topic", "msgs")
		fmt.Fprintf(s.out, "  %s  %s  %s  %s\n", strings.Repeat("─", 4), strings.Repeat("─", 4), strings.Repeat("─", titleWidth), strings.Repeat("─", 8))
		for i, t := range cs.Topics {
			mark := "[ ]"
			if s.st.TopicArchived(forum.ID, t.ID) {
				mark = "[x]"
			}
			name := cleanTitle(t.Title, titleWidth-8) + " #" + strconv.Itoa(t.ID)
			count := "—"
			if t.Count >= 0 {
				count = "~" + groupDigits(t.Count)
			}
			fmt.Fprintf(s.out, "  %4d  %-4s  %s  %s\n", i+1, mark, padRight(name, titleWidth), count)
		}
		fmt.Fprintln(s.out, " 1,3 toggle · a/n all/none · c count messages per topic · Enter back")
		line, ok := s.ask("> ")
		if !ok {
			return false
		}
		switch line = normalizeCommand(line); line {
		case "":
			return true
		case "a", "n":
			s.st.SetAllTopics(forum.ID, ids, line == "a")
		case "c":
			counted := s.details([]stats.Item{s.item(forum)})[0]
			if counted.Err != nil {
				fmt.Fprintf(s.out, "could not count: %v\n", counted.Err)
				continue
			}
			cs.Topics = counted.Topics
			s.keepTopics(forum.ID, counted.Topics)
		default:
			idx, err := selector.ParseIndices(line, len(ids))
			if err != nil {
				fmt.Fprintf(s.out, "error: %v\n", err)
				continue
			}
			s.st.ToggleTopics(forum.ID, ids, idx)
		}
	}
}

func (s *setupDialog) item(d tgclient.Dialog) stats.Item {
	return stats.Item{ID: d.ID, Type: d.Type, Title: d.Title, Peer: d.Peer, ExcludedTopics: s.st.ExcludedFor(d.ID)}
}

// keepTopics remembers a forum's topics for later views and config comments.
func (s *setupDialog) keepTopics(id int64, ts []stats.TopicStats) {
	if s.topics == nil {
		s.topics = map[int64][]stats.TopicStats{}
	}
	s.topics[id] = ts
}

// confirm shows what would be written, with message counts only on request;
// ok is false on EOF or interrupt.
func (s *setupDialog) confirm() (save, ok bool) {
	var items []stats.Item
	for _, d := range s.dialogs {
		if s.st.Chats[d.ID] {
			items = append(items, s.item(d))
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(s.out, "nothing selected")
		return false, true
	}
	fmt.Fprintln(s.out)
	printSelection(s.out, items, s.selected().TopicTitles)
	text, err := s.preview(s.selected())
	if err != nil {
		fmt.Fprintf(s.out, "cannot write the config: %v\n", err)
		return false, true
	}
	rule := strings.Repeat("─", headerWidth)
	fmt.Fprintf(s.out, "\nConfig to write to %s:\n%s\n%s%s\n", s.cfgPath, rule, text, rule)
	for {
		ans, ok := s.ask(fmt.Sprintf("Write to %s (%s)? [y/N, c = count messages first]: ", s.cfgPath, plural(len(items), "chat")))
		if !ok {
			return false, false
		}
		switch strings.ToLower(normalizeCommand(ans)) {
		case "y", "yes", "д", "да":
			return true, true
		case "c":
			counted := s.details(items)
			for _, cs := range counted {
				if cs.Err == nil && cs.Item.Type == store.ChatForum {
					s.keepTopics(cs.Item.ID, cs.Topics)
				}
			}
			fmt.Fprintln(s.out)
			printReport(s.out, counted)
			continue
		}
		fmt.Fprintln(s.out, "not written")
		return false, true
	}
}

// selected turns the dialog state into what the config gets.
func (s *setupDialog) selected() config.Selected {
	out := config.Selected{
		Chats:         s.st.Chats.IDs(),
		ChatTitles:    map[int64]string{},
		ExcludeTopics: map[int64][]int{},
		TopicTitles:   map[int64]map[int]string{},
	}
	for _, d := range s.dialogs {
		out.ChatTitles[d.ID] = d.Title
	}
	for _, id := range out.Chats {
		if ex := s.st.ExcludedFor(id); ex != nil {
			out.ExcludeTopics[id] = ex
		}
	}
	for id, ts := range s.topics {
		for _, t := range ts {
			if out.TopicTitles[id] == nil {
				out.TopicTitles[id] = map[int]string{}
			}
			out.TopicTitles[id][t.ID] = t.Title
		}
	}
	return out
}

func cmdSetup(ctx context.Context, env Env) int {
	st, err := loadSetup(env, false)
	if err == nil {
		in := newLineReader(env.Stdin, func(err error) { fmt.Fprintln(env.Stderr, "read input:", err) })
		_, err = chooseChats(ctx, st, in, env.Stdout, env.Stderr)
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup setup:", err)
		return 1
	}
	return 0
}

// chooseChats runs the chat selection dialog; saved is false when the person
// left without writing the config.
func chooseChats(ctx context.Context, st setup, in *lineReader, out, errOut io.Writer) (saved bool, err error) {
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		raw := c.API()
		api := tgclient.NewAPI(raw)
		self, err := tgclient.SelfID(ctx, raw)
		if err != nil {
			return err
		}
		dialogs, err := api.Dialogs(ctx)
		if err != nil {
			return err
		}
		tgclient.NameSelf(dialogs, self)
		folders, err := api.Folders(ctx, self)
		if err != nil {
			return err
		}
		state, warnings := selector.Initial(dialogs, folders, st.cfg.Targets)
		for _, w := range warnings {
			fmt.Fprintf(out, "current config: %s\n", w)
		}
		progress := progressPrinter(errOut, isTerminal(errOut), "counting")
		d := &setupDialog{
			ctx: ctx, out: out, in: in,
			groups: selector.Groups(dialogs, folders), dialogs: dialogs, st: state, cfgPath: st.paths.Config,
			rowStats:  func(ds []tgclient.Dialog) map[int64]rowInfo { return listRowStats(ctx, api, ds, progress) },
			details:   func(items []stats.Item) []stats.ChatStats { return stats.Collect(ctx, api, items, progress) },
			topicList: func(d tgclient.Dialog) ([]stats.TopicStats, error) { return topicTitles(ctx, api, d) },
			preview: func(sel config.Selected) (string, error) {
				b, err := config.RenderTargets(st.paths.Config, sel)
				return string(b), err
			},
		}
		if !d.run() {
			fmt.Fprintln(out, "config not changed")
			return nil
		}
		sel := d.selected()
		if err := config.WriteTargets(st.paths.Config, sel); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s to %s\n", plural(len(sel.Chats), "chat"), st.paths.Config)
		saved = true
		return nil
	})
	return saved, err
}

func listRowStats(ctx context.Context, api *tgclient.API, ds []tgclient.Dialog, progress func(done, total int)) map[int64]rowInfo {
	out := make(map[int64]rowInfo, len(ds))
	for i, d := range ds {
		var r rowInfo
		r.Msgs, r.Err = api.HistoryCount(ctx, d.Peer)
		if r.Err == nil && d.Type == store.ChatForum {
			// Counted from the topic list, as the report does: the count field of
			// messages.getForumTopics came out one lower on real forums.
			var topics []tgclient.Topic
			topics, r.Err = api.ForumTopics(ctx, d.Peer)
			r.Topics = len(topics)
		}
		out[d.ID] = r
		progress(i+1, len(ds))
	}
	return out
}
