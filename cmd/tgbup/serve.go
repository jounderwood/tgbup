package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/serve"
	"gitlab.com/joeunderwood/tgbup/internal/stats"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func cmdServe(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	listen := fs.String("listen", "127.0.0.1:8640", "address to listen on (this computer only)")
	open := fs.Bool("open", false, "open the page in the browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(env.Stderr, "tgbup serve:", err)
		return 1
	}
	if err := serve.CheckListen(*listen); err != nil {
		return fail(err)
	}
	st, err := loadLocalSetup(env, false)
	if err != nil {
		return fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fail(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	b := &serveBackend{env: env, exe: exe, tg: &tgSession{}}
	defer b.tg.close()
	srv, err := serve.New(b, st.cfg.ArchiveDir, port)
	if err != nil {
		return fail(err)
	}
	url := "http://localhost:" + port + "/"
	fmt.Fprintf(env.Stdout, "tgbup is at %s (this computer only). Ctrl+C stops it.\n", url)
	if *open && runtime.GOOS == "darwin" {
		openURL(url)
	}
	if err := srv.Serve(ctx, ln); err != nil {
		return fail(err)
	}
	return 0
}

// serveBackend gives the web interface the real archive, schedule and Telegram.
type serveBackend struct {
	env Env
	exe string
	tg  *tgSession

	mu     sync.Mutex
	child  *exec.Cmd // a run started from the page, until it exits
	counts map[int64]serve.Count
	topics map[int64][]stats.TopicStats
	// dialogs are loaded once per serve: the chat list does not change often,
	// and loading it again costs a few seconds.
	dialogs []tgclient.Dialog
	folders []tgclient.Folder
}

// load reads the config and secrets again on every request: tgbup init,
// setup or a hand edit may have changed them meanwhile.
func (b *serveBackend) load() (setup, error) { return loadLocalSetup(b.env, false) }

func (b *serveBackend) Status(ctx context.Context) (serve.Status, error) {
	st, err := b.load()
	if err != nil {
		return serve.Status{}, err
	}
	s := serve.Status{Host: hostname(), Now: time.Now(), Bot: st.secrets.BotToken != "" && st.secrets.BotChatID != ""}
	s.Running = b.running(st)
	s.Archive.Dir = tildePath(st.cfg.ArchiveDir, st.paths.Home)
	if _, err := os.Stat(filepath.Join(st.cfg.ArchiveDir, "html", "index.html")); err == nil {
		s.Archive.Site = true
	}
	if err := b.archiveStatus(ctx, st, &s); err != nil {
		return s, err
	}
	s.Schedule = scheduleStatus(st, b.env, time.Now())
	s.Problems = setupProblems(st, s)
	return s, nil
}

func (b *serveBackend) archiveStatus(ctx context.Context, st setup, s *serve.Status) error {
	for _, r := range st.cfg.Backup.Repositories {
		s.Backups = append(s.Backups, serve.Backup{Repo: tildePath(r, st.paths.Home)})
	}
	path := filepath.Join(st.cfg.ArchiveDir, "archive.sqlite")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := store.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	a := &s.Archive
	if a.Messages, err = db.MessageTotal(ctx); err != nil {
		return err
	}
	chats, err := db.RenderChats(ctx)
	if err != nil {
		return err
	}
	a.Chats = len(chats)
	for status, n := range map[string]*int{"done": &a.Files, "pending": &a.PendingFiles, "failed": &a.FailedFiles} {
		if *n, err = db.CountMedia(ctx, status); err != nil {
			return err
		}
	}
	a.Bytes = archiveSize(st.cfg.ArchiveDir)
	for i, r := range st.cfg.Backup.Repositories {
		s.Backups[i].LastSuccess, _ = backup.LastSuccess(ctx, db, r)
	}
	runs, err := db.RunsSince(ctx, time.Time{})
	if err != nil {
		return err
	}
	for i := len(runs) - 1; i >= 0 && len(s.Runs) < 20; i-- {
		r := runs[i]
		var rec runRecord
		_ = json.Unmarshal([]byte(r.Summary), &rec)
		s.Runs = append(s.Runs, serve.Run{Started: r.Started, Finished: r.Finished, OK: r.OK,
			NewMessages: rec.NewMessages, NewMedia: rec.NewMedia, Errors: append(rec.SyncErrors, rec.BackupErrors...)})
	}
	return nil
}

func scheduleStatus(st setup, env Env, now time.Time) serve.Schedule {
	sc := serve.Schedule{Times: runTimes(), Next: nextRun(now)}
	if runtime.GOOS != "darwin" {
		sc.Note = "Not on macOS: start tgbup run from cron or systemd"
		return sc
	}
	if j, err := newLaunchdJob(st.paths, env); err == nil {
		sc.Installed = j.installedHere() && j.state().Loaded
	}
	return sc
}

// nextRun is the next scheduled start after now.
func nextRun(now time.Time) time.Time {
	for day := 0; day < 2; day++ {
		for _, h := range runHours {
			t := time.Date(now.Year(), now.Month(), now.Day()+day, h, 30, 0, 0, now.Location())
			if t.After(now) {
				return t
			}
		}
	}
	return time.Time{}
}

// setupProblems lists what tgbup init has not set up yet.
func setupProblems(st setup, s serve.Status) []string {
	var out []string
	add := func(ok bool, what string) {
		if !ok {
			out = append(out, what+": run tgbup init in Terminal")
		}
	}
	add(st.secrets.APIID != 0 && st.secrets.APIHash != "", "Telegram is not connected")
	add(len(st.cfg.Targets.Include) > 0 || len(st.cfg.Targets.Folders) > 0, "No chats chosen")
	add(st.secrets.ResticPassword != "" && len(st.cfg.Backup.Repositories) > 0, "Backups are not set up")
	add(s.Schedule.Installed || s.Schedule.Note != "" || st.testDC, "Automatic updates are not scheduled")
	return out
}

// running reports a run in progress, started here or by the schedule.
func (b *serveBackend) running(st setup) bool {
	b.mu.Lock()
	child := b.child != nil
	b.mu.Unlock()
	return child || lockHeld(st.paths.Lock())
}

// lockHeld probes run.lock. The probe holds the lock for an instant; a run
// starting at that moment waits for it (see lockPatience) instead of skipping.
func lockHeld(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	release, err := tryLock(path)
	if err != nil {
		return errors.Is(err, errLocked)
	}
	release()
	return false
}

func (b *serveBackend) StartRun() error {
	st, err := b.load()
	if err != nil {
		return err
	}
	if b.running(st) {
		return serve.ErrBusy
	}
	logPath := filepath.Join(st.paths.StateDir, "logs", "run.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "\n── started from tgbup serve at %s ──\n", time.Now().Format("2006-01-02 15:04:05"))
	cmd := exec.Command(b.exe, "run")
	cmd.Stdout, cmd.Stderr = f, f
	// Its own process group: Ctrl+C on serve must not stop a run half-way.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	b.mu.Lock()
	b.child = cmd
	b.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		f.Close()
		b.mu.Lock()
		b.child = nil
		b.mu.Unlock()
	}()
	return nil
}

func (b *serveBackend) RunLog() (string, error) {
	st, err := b.load()
	if err != nil {
		return "", err
	}
	return tailFile(filepath.Join(st.paths.StateDir, "logs", "run.log"), 64<<10, 200)
}

// tailFile returns up to the last n lines from the last limit bytes of path.
func tailFile(path string, limit int64, n int) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	from := max(0, st.Size()-limit)
	buf := make([]byte, st.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if from > 0 {
		// Drop the line cut in half.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n"), nil
}

// chatData loads the dialogs and folders once and returns the setup with them.
func (b *serveBackend) chatData(ctx context.Context) (setup, *tgclient.API, error) {
	st, err := loadSetup(b.env, false)
	if err != nil {
		return st, nil, err
	}
	api, raw, err := b.tg.get(ctx, st.clientOptions())
	if err != nil {
		return st, nil, err
	}
	b.mu.Lock()
	loaded := b.dialogs != nil
	b.mu.Unlock()
	if loaded {
		return st, api, nil
	}
	self, err := tgclient.SelfID(ctx, raw)
	if err != nil {
		return st, nil, err
	}
	dialogs, err := api.Dialogs(ctx)
	if err != nil {
		return st, nil, err
	}
	tgclient.NameSelf(dialogs, self)
	folders, err := api.Folders(ctx, self)
	if err != nil {
		return st, nil, err
	}
	b.mu.Lock()
	b.dialogs, b.folders = dialogs, folders
	b.mu.Unlock()
	return st, api, nil
}

func (b *serveBackend) dialog(id int64) (tgclient.Dialog, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, d := range b.dialogs {
		if d.ID == id {
			return d, true
		}
	}
	return tgclient.Dialog{}, false
}

func (b *serveBackend) Chats(ctx context.Context) (serve.ChatList, error) {
	st, _, err := b.chatData(ctx)
	if err != nil {
		return serve.ChatList{}, err
	}
	b.mu.Lock()
	dialogs, folders := b.dialogs, b.folders
	b.mu.Unlock()
	state, _ := selector.Initial(dialogs, folders, st.cfg.Targets)
	var out serve.ChatList
	for _, g := range selector.Groups(dialogs, folders) {
		ids := make([]int64, len(g.Chats))
		for i, c := range g.Chats {
			ids[i] = c.ID
		}
		out.Groups = append(out.Groups, serve.Group{Title: g.Title, IDs: ids})
	}
	for _, d := range dialogs {
		ex := state.ExcludedFor(d.ID)
		if ex == nil {
			ex = []int{}
		}
		out.Chats = append(out.Chats, serve.Chat{ID: d.ID, Title: d.Title, Type: d.Type, Username: d.Username,
			Selected: state.Chats[d.ID], ExcludedTopics: ex})
	}
	return out, nil
}

func (b *serveBackend) Counts(ctx context.Context, ids []int64) (map[int64]serve.Count, error) {
	_, api, err := b.chatData(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]serve.Count{}
	for _, id := range ids {
		b.mu.Lock()
		c, ok := b.counts[id]
		b.mu.Unlock()
		if !ok {
			d, found := b.dialog(id)
			if !found {
				continue
			}
			r := listRowStats(ctx, api, []tgclient.Dialog{d}, func(int, int) {})[id]
			c = serve.Count{Messages: r.Msgs, Topics: r.Topics}
			if r.Err != nil {
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				c.Err = r.Err.Error()
			}
			b.mu.Lock()
			if b.counts == nil {
				b.counts = map[int64]serve.Count{}
			}
			if r.Err == nil {
				b.counts[id] = c
			}
			b.mu.Unlock()
		}
		out[id] = c
	}
	return out, nil
}

func (b *serveBackend) Topics(ctx context.Context, id int64) ([]serve.Topic, error) {
	_, api, err := b.chatData(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	ts, ok := b.topics[id]
	b.mu.Unlock()
	if !ok {
		d, found := b.dialog(id)
		if !found || d.Type != store.ChatForum {
			return nil, fmt.Errorf("chat %d is not a forum", id)
		}
		cs := stats.Collect(ctx, api, []stats.Item{{ID: d.ID, Type: d.Type, Title: d.Title, Peer: d.Peer}}, func(int, int) {})[0]
		if cs.Err != nil {
			return nil, cs.Err
		}
		ts = cs.Topics
		b.mu.Lock()
		if b.topics == nil {
			b.topics = map[int64][]stats.TopicStats{}
		}
		b.topics[id] = ts
		b.mu.Unlock()
	}
	out := make([]serve.Topic, len(ts))
	for i, t := range ts {
		out[i] = serve.Topic{ID: t.ID, Title: t.Title, Messages: t.Count}
	}
	return out, nil
}

// selected turns a page selection into what the config gets, rejecting chats
// that are not in the list.
func (b *serveBackend) selected(sel serve.Selection) (config.Selected, []stats.Item, error) {
	out := config.Selected{ChatTitles: map[int64]string{}, ExcludeTopics: map[int64][]int{}, TopicTitles: map[int64]map[int]string{}}
	var items []stats.Item
	for _, id := range sel.Chats {
		d, ok := b.dialog(id)
		if !ok {
			return out, nil, fmt.Errorf("chat %d is not in your chat list", id)
		}
		if slices.Contains(out.Chats, id) {
			continue
		}
		out.Chats = append(out.Chats, id)
		out.ChatTitles[id] = d.Title
		it := stats.Item{ID: d.ID, Type: d.Type, Title: d.Title, Peer: d.Peer}
		if ex := sel.ExcludeTopics[id]; len(ex) > 0 && d.Type == store.ChatForum {
			out.ExcludeTopics[id] = slices.Sorted(slices.Values(ex))
			it.ExcludedTopics = out.ExcludeTopics[id]
		}
		b.mu.Lock()
		for _, t := range b.topics[id] {
			if out.TopicTitles[id] == nil {
				out.TopicTitles[id] = map[int]string{}
			}
			out.TopicTitles[id][t.ID] = t.Title
		}
		b.mu.Unlock()
		items = append(items, it)
	}
	return out, items, nil
}

func (b *serveBackend) Preview(ctx context.Context, sel serve.Selection) (serve.Preview, error) {
	st, api, err := b.chatData(ctx)
	if err != nil {
		return serve.Preview{}, err
	}
	cfgSel, items, err := b.selected(sel)
	if err != nil {
		return serve.Preview{}, err
	}
	var report bytes.Buffer
	printReport(&report, stats.Collect(ctx, api, items, func(int, int) {}))
	text, err := config.RenderTargets(st.paths.Config, cfgSel)
	if err != nil {
		return serve.Preview{}, err
	}
	return serve.Preview{Report: report.String(), Config: string(text)}, nil
}

func (b *serveBackend) Save(ctx context.Context, sel serve.Selection) error {
	st, _, err := b.chatData(ctx)
	if err != nil {
		return err
	}
	cfgSel, _, err := b.selected(sel)
	if err != nil {
		return err
	}
	return config.WriteTargets(st.paths.Config, cfgSel)
}

// tgSession keeps one Telegram connection open while serve runs, so the chat
// page does not reconnect for every request.
type tgSession struct {
	mu   sync.Mutex
	api  *tgclient.API
	raw  *tg.Client
	stop context.CancelFunc
	done chan struct{}
}

func (t *tgSession) get(ctx context.Context, opts tgclient.Options) (*tgclient.API, *tg.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.api != nil {
		return t.api, t.raw, nil
	}
	runCtx, stop := context.WithCancel(context.Background())
	type conn struct {
		raw *tg.Client
		err error
	}
	ready := make(chan conn, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := tgclient.Run(runCtx, opts, func(ctx context.Context, c *telegram.Client) error {
			if err := tgclient.RequireAuth(ctx, c); err != nil {
				return err
			}
			ready <- conn{raw: c.API()}
			<-ctx.Done()
			return nil
		})
		if err == nil {
			err = errors.New("the Telegram connection closed")
		}
		select {
		case ready <- conn{err: err}:
		default:
		}
		t.mu.Lock()
		if t.done == done {
			t.api, t.raw, t.stop, t.done = nil, nil, nil, nil
		}
		t.mu.Unlock()
	}()
	select {
	case c := <-ready:
		if c.err != nil {
			stop()
			return nil, nil, c.err
		}
		t.api, t.raw, t.stop, t.done = tgclient.NewAPI(c.raw), c.raw, stop, done
		return t.api, t.raw, nil
	case <-ctx.Done():
		stop()
		return nil, nil, ctx.Err()
	}
}

func (t *tgSession) close() {
	t.mu.Lock()
	stop, done := t.stop, t.done
	t.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}
