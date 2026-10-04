package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	if _, err := loadLocalSetup(env, false); err != nil {
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
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	b := &serveBackend{env: env, exe: exe, tg: newTGSession(ctx, 5*time.Minute), log: newLogger(env.Stderr)}
	defer b.dropLogin(nil)
	srv, err := serve.New(b, port)
	if err != nil {
		return fail(err)
	}
	srv.Log = b.log
	shown, _, _ := net.SplitHostPort(*listen)
	url := "http://" + net.JoinHostPort(shown, port) + "/"
	fmt.Fprintf(env.Stdout, "tgbup is at %s (this computer only). Ctrl+C stops it.\n", url)
	if *open && runtime.GOOS == "darwin" {
		// Until the archive can be filled, the page to start from is Settings.
		if s, err := b.SetupState(ctx); err == nil && !s.(setupState).Complete {
			url += "settings"
		}
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

	// dbMu keeps the page's own reads of the archive away from a restore that
	// swaps the database (and its WAL files) underneath them.
	dbMu sync.RWMutex
	// probeMu serializes run.lock probes: two in this process would see each
	// other's lock as a run in progress.
	probeMu sync.Mutex

	mu     sync.Mutex
	child  *exec.Cmd // a run started from the page, until it exits
	counts map[int64]serve.Count
	topics map[int64][]stats.TopicStats
	// dialogs are kept for a few minutes: loading them costs seconds, but
	// chats joined meanwhile must show up.
	dialogs   []tgclient.Dialog
	folders   []tgclient.Folder
	dialogsAt time.Time
	// login is a web sign-in in progress; loggedIn caches a session known to work.
	login     *webLogin
	loggedIn  bool
	loginMu   sync.Mutex
	dialLogin func(context.Context, setup) (authFlow, func(), error) // tests replace it
	log       *slog.Logger                                           // nil in tests
	newJob    func(setup) (launchdJob, error)                        // tests replace launchctl
	disk      struct {
		dir, warning string
		at           time.Time
	}
}

const dialogsTTL = 5 * time.Minute

// load reads the config and secrets again on every request: tgbup init,
// setup or a hand edit may have changed them meanwhile.
func (b *serveBackend) load() (setup, error) { return loadLocalSetup(b.env, false) }

func (b *serveBackend) Status(ctx context.Context) (serve.Status, error) {
	b.dbMu.RLock()
	defer b.dbMu.RUnlock()
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
	s.SetupLeft = setupProblems(st, s)
	if w := b.diskWarning(ctx, st.cfg.ArchiveDir); w != "" {
		s.Problems = append(s.Problems, w)
	}
	return s, nil
}

func (b *serveBackend) archiveStatus(ctx context.Context, st setup, s *serve.Status) error {
	for _, r := range st.cfg.Backup.Repositories {
		shown := tildePath(r, st.paths.Home)
		if checkRepo(r, st.secrets.ResticPassword) != nil {
			shown = "a place that is not a full path (fix it in Settings → Backups)" // may be a pasted password
		}
		s.Backups = append(s.Backups, serve.Backup{Repo: shown})
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
			NewMessages: rec.NewMessages, NewMedia: rec.NewMedia, Errors: append(rec.SyncErrors, rec.BackupErrors...), Log: rec.Log})
	}
	return nil
}

func scheduleStatus(st setup, env Env, now time.Time) serve.Schedule {
	times := st.cfg.Schedule.Clocks()
	sc := serve.Schedule{Times: strings.Split(clockList(times), ", "), Next: nextRun(now, times)}
	if runtime.GOOS != "darwin" {
		sc.Note = "Not on macOS: start tgbup run from cron or systemd"
		return sc
	}
	if j, err := newLaunchdJob(st, env); err == nil {
		sc.Installed = j.installedHere() && j.state().Loaded
	}
	return sc
}

// nextRun is the next scheduled start after now.
func nextRun(now time.Time, times []config.Clock) time.Time {
	for day := 0; day < 2; day++ {
		for _, c := range times {
			t := time.Date(now.Year(), now.Month(), now.Day()+day, c.Hour, c.Minute, 0, 0, now.Location())
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
			out = append(out, what+".")
		}
	}
	add(st.secrets.APIID != 0 && st.secrets.APIHash != "", "Telegram is not connected")
	add(len(st.cfg.Targets.Include) > 0 || len(st.cfg.Targets.Folders) > 0, "No chats chosen")
	add(s.Schedule.Installed || s.Schedule.Note != "" || st.testDC, "Automatic updates are not scheduled")
	return out
}

// running reports a run in progress, started here or by the schedule; the
// caller holds b.mu.
func (b *serveBackend) runningLocked(st setup) bool {
	if b.child != nil {
		return true
	}
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	return lockHeld(st.paths.Lock())
}

func (b *serveBackend) running(st setup) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runningLocked(st)
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

func (b *serveBackend) StartRun(takeout bool) error {
	if takeout {
		return b.startProcess("run", "--takeout")
	}
	return b.startProcess("run")
}

// startProcess runs tgbup with args as its own process, writing to run.log
// like the schedule does; one at a time, and not while a run holds run.lock.
func (b *serveBackend) startProcess(args ...string) error {
	st, err := b.load()
	if err != nil {
		return err
	}
	// Checked and started under one lock, so two clicks start one run.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runningLocked(st) {
		return serve.ErrBusy
	}
	logPath := st.paths.RunLog()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	rotateLog(logPath)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "\n── tgbup %s, started from tgbup serve at %s ──\n", strings.Join(args, " "), time.Now().Format("2006-01-02 15:04:05"))
	cmd := exec.Command(b.exe, args...)
	cmd.Stdout, cmd.Stderr = f, f
	// Its own process group: Ctrl+C on serve must not stop a run half-way.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	b.child = cmd
	what := "tgbup " + strings.Join(args, " ")
	b.logger().Info("started "+what, "log", logPath)
	go func() {
		if err := cmd.Wait(); err != nil {
			b.logger().Warn(what+" failed, see the log", "err", err, "log", logPath)
		} else {
			b.logger().Info(what + " finished")
		}
		f.Close()
		b.mu.Lock()
		if b.child == cmd {
			b.child = nil
		}
		b.mu.Unlock()
	}()
	return nil
}

func (b *serveBackend) RunLog() (string, error) {
	st, err := b.load()
	if err != nil {
		return "", err
	}
	return tailFile(st.paths.RunLog(), 64<<10, 200)
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
	loaded := b.dialogs != nil && time.Since(b.dialogsAt) < dialogsTTL
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
	b.dialogs, b.folders, b.dialogsAt = dialogs, folders, time.Now()
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
	state, warnings := selector.Initial(dialogs, folders, st.cfg.Targets)
	var out serve.ChatList
	for _, w := range warnings {
		out.Warnings = append(out.Warnings, "In the current config: "+w+". Saving here leaves it out.")
	}
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

func (b *serveBackend) Topics(ctx context.Context, id int64, counts bool) ([]serve.Topic, error) {
	_, api, err := b.chatData(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	ts, ok := b.topics[id]
	b.mu.Unlock()
	if !ok || counts && !counted(ts) {
		d, found := b.dialog(id)
		if !found || d.Type != store.ChatForum {
			return nil, fmt.Errorf("chat %d is not a forum", id)
		}
		if counts {
			cs := stats.Collect(ctx, api, []stats.Item{{ID: d.ID, Type: d.Type, Title: d.Title, Peer: d.Peer}}, func(int, int) {})[0]
			if cs.Err != nil {
				return nil, cs.Err
			}
			ts = cs.Topics
		} else if ts, err = topicTitles(ctx, api, d); err != nil {
			return nil, err
		}
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

// topicTitles lists a forum's topics with one request, uncounted (Count -1).
func topicTitles(ctx context.Context, api *tgclient.API, d tgclient.Dialog) ([]stats.TopicStats, error) {
	topics, err := api.ForumTopics(ctx, d.Peer)
	if err != nil {
		return nil, err
	}
	out := make([]stats.TopicStats, len(topics))
	for i, t := range topics {
		out[i] = stats.TopicStats{ID: t.ID, Title: t.Title, Count: -1}
	}
	return out, nil
}

func counted(ts []stats.TopicStats) bool {
	for _, t := range ts {
		if t.Count < 0 {
			return false
		}
	}
	return true
}

// selected turns a page selection into what the config gets, rejecting chats
// that are not in the list.
func (b *serveBackend) selected(sel serve.Selection) (config.Selected, []stats.Item, error) {
	out := config.Selected{ChatTitles: map[int64]string{}, ExcludeTopics: map[int64][]int{}, TopicTitles: map[int64]map[int]string{}}
	var items []stats.Item
	for _, id := range sel.Chats {
		d, ok := b.dialog(id)
		if !ok {
			return out, nil, fmt.Errorf("%w: chat %d is not in your chat list (reload the page)", serve.ErrBadRequest, id)
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

func (b *serveBackend) Preview(ctx context.Context, sel serve.Selection, counts bool) (serve.Preview, error) {
	st, api, err := b.chatData(ctx)
	if err != nil {
		return serve.Preview{}, err
	}
	_, items, err := b.selected(sel)
	if err != nil {
		return serve.Preview{}, err
	}
	var report bytes.Buffer
	if counts {
		collected := stats.Collect(ctx, api, items, func(int, int) {})
		b.mu.Lock()
		for _, cs := range collected {
			if cs.Err == nil && cs.Item.Type == store.ChatForum {
				if b.topics == nil {
					b.topics = map[int64][]stats.TopicStats{}
				}
				b.topics[cs.Item.ID] = cs.Topics
			}
		}
		b.mu.Unlock()
		printReport(&report, collected)
	} else {
		// Left-out topics are named in the config by their titles: one request
		// per forum whose topics were not listed yet.
		for _, it := range items {
			b.mu.Lock()
			_, known := b.topics[it.ID]
			b.mu.Unlock()
			if len(it.ExcludedTopics) == 0 || known {
				continue
			}
			if d, ok := b.dialog(it.ID); ok {
				if ts, err := topicTitles(ctx, api, d); err == nil {
					b.mu.Lock()
					if b.topics == nil {
						b.topics = map[int64][]stats.TopicStats{}
					}
					b.topics[it.ID] = ts
					b.mu.Unlock()
				}
			}
		}
	}
	cfgSel, _, err := b.selected(sel)
	if err != nil {
		return serve.Preview{}, err
	}
	if !counts {
		printSelection(&report, items, cfgSel.TopicTitles)
	}
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

// tgSession keeps one Telegram connection open while the chat page is in
// use and closes it after idle: a re-login elsewhere (tgbup init) then takes
// effect, and the session is not held open for nothing.
type tgSession struct {
	base context.Context // serve's: shutting down closes the connection
	idle time.Duration

	mu         sync.Mutex
	api        *tgclient.API
	raw        *tg.Client
	stop       context.CancelFunc
	done       chan struct{}
	connecting chan struct{} // closed when a connect attempt ends
	connErr    error
	lastUse    time.Time
}

func newTGSession(base context.Context, idle time.Duration) *tgSession {
	return &tgSession{base: base, idle: idle}
}

func (t *tgSession) get(ctx context.Context, opts tgclient.Options) (*tgclient.API, *tg.Client, error) {
	for {
		t.mu.Lock()
		if t.api != nil {
			t.lastUse = time.Now()
			api, raw := t.api, t.raw
			t.mu.Unlock()
			return api, raw, nil
		}
		wait := t.connecting
		if wait == nil {
			wait = make(chan struct{})
			t.connecting = wait
			go t.connect(opts, wait)
		}
		t.mu.Unlock()
		// Not waiting under t.mu: a slow connect must not block shutdown.
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		t.mu.Lock()
		err, ok := t.connErr, t.api != nil
		t.mu.Unlock()
		if !ok {
			if err == nil {
				err = errors.New("the Telegram connection closed")
			}
			return nil, nil, err
		}
	}
}

// connect runs one connection until it fails, the serve context ends or it
// stays unused for t.idle.
func (t *tgSession) connect(opts tgclient.Options, connecting chan struct{}) {
	runCtx, stop := context.WithCancel(t.base)
	done := make(chan struct{})
	announced := false
	announce := func(raw *tg.Client, err error) {
		t.mu.Lock()
		if raw != nil {
			t.api, t.raw, t.stop, t.done, t.lastUse = tgclient.NewAPI(raw), raw, stop, done, time.Now()
		}
		t.connErr, t.connecting = err, nil
		t.mu.Unlock()
		close(connecting)
		announced = true
	}
	err := tgclient.Run(runCtx, opts, func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		announce(c.API(), nil)
		tick := time.NewTicker(min(t.idle, time.Minute))
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				t.mu.Lock()
				idle := time.Since(t.lastUse) > t.idle
				t.mu.Unlock()
				if idle {
					return nil
				}
			}
		}
	})
	stop()
	if !announced {
		if err == nil {
			err = errors.New("the Telegram connection closed")
		}
		announce(nil, err)
	}
	t.mu.Lock()
	if t.done == done {
		t.api, t.raw, t.stop, t.done = nil, nil, nil, nil
	}
	t.mu.Unlock()
	close(done)
}

// logger is the backend's log; tests build backends without one.
func (b *serveBackend) logger() *slog.Logger {
	if b.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return b.log
}

// OpenArchive shows html/index.html in the default browser: the pages work
// straight from the disk.
func (b *serveBackend) OpenArchive() error {
	st, err := b.load()
	if err != nil {
		return err
	}
	index := filepath.Join(st.cfg.ArchiveDir, "html", "index.html")
	if !isFile(index) {
		return fmt.Errorf("%w: the archive has no pages yet: they are drawn after the first download", serve.ErrBadRequest)
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("%w: open %s in a browser", serve.ErrBadRequest, index)
	}
	return exec.Command("open", index).Run()
}

// Snapshots lists the snapshots in a backup place, by its position in the
// config (the page never sends a place itself).
func (b *serveBackend) Snapshots(ctx context.Context, place int) ([]serve.Snapshot, error) {
	st, err := b.load()
	if err != nil {
		return nil, err
	}
	repos := st.cfg.Backup.Repositories
	if place < 0 || place >= len(repos) {
		return nil, fmt.Errorf("%w: no backup place %d", serve.ErrBadRequest, place)
	}
	if checkRepo(repos[place], st.secrets.ResticPassword) != nil {
		return nil, errBadPlace
	}
	o, err := backupOptions(ctx, st)
	if err != nil {
		return nil, err
	}
	snaps, err := backup.Snapshots(ctx, o, repos[place])
	if err != nil {
		return nil, err
	}
	out := make([]serve.Snapshot, len(snaps))
	for i, sn := range snaps {
		out[i] = serve.Snapshot{ID: sn.ShortID, Time: sn.Time, Host: sn.Host}
	}
	return out, nil
}

// diskWarning is cached: the page asks for the status every half minute, and
// whether the disk is encrypted changes rarely.
func (b *serveBackend) diskWarning(ctx context.Context, archiveDir string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disk.dir != archiveDir || time.Since(b.disk.at) > 10*time.Minute {
		b.disk.dir, b.disk.at, b.disk.warning = archiveDir, time.Now(), diskWarning(ctx, archiveDir)
	}
	return b.disk.warning
}
