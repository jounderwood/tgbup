package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

// The web setup: the same steps as tgbup init, through the same step code.

type setupStep struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional"`
	Status   string `json:"status"`
	// Unavailable explains a step that cannot be taken here (test servers, not macOS).
	Unavailable string `json:"unavailable,omitempty"`
}

type setupState struct {
	Steps []setupStep `json:"steps"`
	// Complete: the archive can be filled (keys, login, archive folder, chats).
	Complete bool         `json:"complete"`
	Running  bool         `json:"running"`
	Archive  archiveForm  `json:"archive"`
	Backup   backupForm   `json:"backup"`
	Login    loginForm    `json:"login"`
	Schedule scheduleForm `json:"schedule"`
	// Warnings are shown above the steps (an unencrypted disk).
	Warnings []string `json:"warnings"`
}

type archiveForm struct {
	Dir         string `json:"dir"`
	Default     string `json:"default"`
	Configured  bool   `json:"configured"`
	HasMessages bool   `json:"has_messages"`
}

type backupForm struct {
	Local       string          `json:"local"`
	DefaultDir  string          `json:"default_dir"`
	Extras      []string        `json:"extras"`
	Choices     []storageChoice `json:"choices"`
	HasPassword bool            `json:"has_password"`
	// Places lists where a backup to restore from may be.
	Places []storageChoice `json:"places"`
}

type loginForm struct {
	// CodeSent: a code is on its way and the page should ask for it.
	CodeSent       bool `json:"code_sent"`
	PasswordNeeded bool `json:"password_needed"`
}

type scheduleForm struct {
	Times   []string `json:"times"`
	LastRun *runInfo `json:"last_run,omitempty"`
}

type runInfo struct {
	Started time.Time `json:"started"`
	OK      bool      `json:"ok"`
}

func (b *serveBackend) quietWizard(out io.Writer) *wizard {
	w := &wizard{env: b.env, out: out, assumeYes: true}
	w.initRepos = w.createRepos
	return w
}

// SetupState reports every step; a step whose check fails shows the error as its status.
func (b *serveBackend) SetupState(ctx context.Context) (any, error) {
	b.dbMu.RLock()
	defer b.dbMu.RUnlock()
	w := b.quietWizard(io.Discard)
	var s setupState
	add := func(id, title string, optional bool, check func(context.Context) (string, bool, error)) *setupStep {
		status, done, err := check(ctx)
		if err != nil {
			status = err.Error()
		}
		s.Steps = append(s.Steps, setupStep{ID: id, Title: title, Done: done, Optional: optional, Status: status})
		return &s.Steps[len(s.Steps)-1]
	}
	add("keys", "Telegram API keys", false, w.checkKeys)
	add("login", "Telegram login", false, b.checkLoginCached)
	add("archive", "Archive folder", false, w.checkArchiveDir)
	add("chats", "Chats to archive", false, w.checkChats)
	add("backup", "Backups", true, w.checkStorage)
	add("bot", "Alerts bot", true, w.checkBot)
	add("download", "First download", false, w.checkArchive)
	sched := add("schedule", "Automatic updates", true, w.checkSchedule)
	s.Complete = true
	for _, st := range s.Steps[:4] {
		s.Complete = s.Complete && st.Done
	}

	st, err := w.load()
	if err != nil {
		// The keys step fixes broken secrets; the rest needs them.
		return s, nil
	}
	if st.testDC {
		sched.Unavailable = "not on Telegram's test servers: the scheduled job runs the real archive"
	} else if runtime.GOOS != "darwin" {
		sched.Unavailable = "only on macOS for now: start tgbup run from cron or systemd"
	}
	home := st.paths.Home
	s.Running = b.running(st)
	_, cfgErr := os.Stat(st.paths.Config)
	s.Archive = archiveForm{Dir: tildePath(st.cfg.ArchiveDir, home), Default: tildePath(st.cfg.ArchiveDir, home), Configured: cfgErr == nil}
	if n, err := archivedMessages(ctx, st.cfg.ArchiveDir); err == nil {
		s.Archive.HasMessages = n > 0
	}
	// A place that is not one (a password pasted by mistake) is left out:
	// saving the form drops it.
	var places []string
	for _, r := range st.cfg.Backup.Repositories {
		if checkRepo(r, st.secrets.ResticPassword) == nil {
			places = append(places, r)
		}
	}
	local, extras := splitRepos(places, home)
	s.Backup = backupForm{Local: tildePath(local, home), DefaultDir: backupDirDefault(st), HasPassword: st.secrets.ResticPassword != "",
		Choices: storageChoices(home, "/Volumes")}
	for _, e := range extras {
		s.Backup.Extras = append(s.Backup.Extras, tildePath(e, home))
	}
	for i := range s.Backup.Choices {
		s.Backup.Choices[i].Repo = tildePath(s.Backup.Choices[i].Repo, home)
	}
	if def := backupDirDefault(st); isDir(expandTilde(def, home)) {
		s.Backup.Places = append(s.Backup.Places, storageChoice{Label: def + " (folder on this Mac)", Repo: def})
	}
	s.Backup.Places = append(s.Backup.Places, s.Backup.Choices...)
	b.mu.Lock()
	if b.login != nil {
		s.Login = loginForm{CodeSent: b.login.hash != "", PasswordNeeded: b.login.password}
	}
	b.mu.Unlock()
	s.Schedule.Times = strings.Split(clockList(st.cfg.Schedule.Clocks()), ", ")
	if w := b.diskWarning(ctx, st.cfg.ArchiveDir); w != "" {
		s.Warnings = append(s.Warnings, w)
	}
	if r, ok, err := lastRun(ctx, st.cfg.ArchiveDir); err == nil && ok {
		s.Schedule.LastRun = &runInfo{Started: r.Started, OK: r.OK}
	}
	return s, nil
}

// checkLoginCached asks Telegram only until the session is known to work:
// the page reads the state after every action.
func (b *serveBackend) checkLoginCached(ctx context.Context) (string, bool, error) {
	b.mu.Lock()
	ok, signingIn := b.loggedIn, b.login != nil
	b.mu.Unlock()
	if ok {
		return "logged in", true, nil
	}
	if signingIn {
		// A second client on the same session file would race the sign-in's.
		return "signing in…", false, nil
	}
	status, done, err := b.quietWizard(io.Discard).checkLogin(ctx)
	if done {
		b.mu.Lock()
		b.loggedIn = true
		b.mu.Unlock()
	}
	return status, done, err
}

// SetupAction runs one step's action with the JSON the page sent.
func (b *serveBackend) SetupAction(ctx context.Context, name string, body []byte) (any, error) {
	var req struct {
		APIID    string   `json:"api_id"`
		APIHash  string   `json:"api_hash"`
		Phone    string   `json:"phone"`
		Code     string   `json:"code"`
		Password string   `json:"password"`
		Dir      string   `json:"dir"`
		Enabled  bool     `json:"enabled"`
		Local    string   `json:"local"`
		Extras   []string `json:"extras"`
		Token    string   `json:"token"`
		ChatID   string   `json:"chat_id"`
		Takeout  bool     `json:"takeout"`
		Times    string   `json:"times"`
		Repo     string   `json:"repo"`
		Snapshot string   `json:"snapshot"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, badInput("could not read the form: %v", err)
		}
	}
	if name == "keys" {
		_, err := saveKeys(b.env, req.APIID, req.APIHash)
		if err == nil {
			// Other keys: the session may not be valid any more.
			b.dropLogin(nil)
			b.mu.Lock()
			b.loggedIn, b.dialogs = false, nil
			b.mu.Unlock()
		}
		return nil, err
	}
	if strings.HasPrefix(name, "login/") {
		// One sign-in step at a time: two "Send code" clicks must not open two connections.
		b.loginMu.Lock()
		defer b.loginMu.Unlock()
	}
	st, err := b.load()
	if err != nil {
		return nil, err
	}
	switch name {
	case "login/code":
		return nil, b.loginSendCode(ctx, st, req.Phone)
	case "login/signin":
		return b.loginSignIn(ctx, req.Code)
	case "login/password":
		return b.loginPassword(ctx, req.Password)
	case "archive":
		if req.Dir == "" {
			req.Dir = st.cfg.ArchiveDir // the default until a config sets one
		}
		return nil, saveArchiveDir(st, req.Dir)
	case "archive/move":
		// The page reads the database meanwhile; it waits for the move.
		b.dbMu.Lock()
		defer b.dbMu.Unlock()
		return nil, moveArchive(st, req.Dir)
	case "choose-folder":
		return chooseFolder(ctx)
	case "password/new":
		return map[string]string{"password": newPassword()}, nil
	case "backup":
		return b.saveBackupForm(ctx, st, req.Enabled, req.Local, req.Extras, req.Password)
	case "bot/check":
		name, err := (notify.Bot{Token: strings.TrimSpace(req.Token)}).Me(ctx)
		if err != nil {
			return nil, badInput("%v", err)
		}
		return map[string]string{"name": name}, nil
	case "bot/wait":
		// One long poll; the page asks again until a message comes or the person gives up.
		wait, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		chat, err := (notify.Bot{Token: strings.TrimSpace(req.Token)}).PrivateChat(wait, 20*time.Second)
		if errors.Is(err, context.DeadlineExceeded) {
			return map[string]any{"waiting": true}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"chat_id": strconv.FormatInt(chat.ID, 10), "name": chat.Name}, nil
	case "bot/save":
		return nil, saveBot(ctx, st, notify.Bot{Token: strings.TrimSpace(req.Token), ChatID: req.ChatID})
	case "download":
		// A whole run: the first download is recorded and backed up like any other.
		args := []string{"run", "--trigger=page"}
		if req.Takeout {
			args = append(args, "--takeout")
		}
		return nil, b.startProcess(args...)
	case "schedule/install":
		// With times: save them, then (re)install the job so launchd uses them.
		if st.testDC {
			return nil, badInput("not on Telegram's test servers: the scheduled job runs the real archive")
		}
		before := clockList(st.cfg.Schedule.Clocks())
		if req.Times != "" {
			if err := saveScheduleTimes(st, req.Times); err != nil {
				return nil, err
			}
			if st, err = b.load(); err != nil {
				return nil, err
			}
		}
		j, err := b.launchd(st)
		if err != nil {
			return nil, err
		}
		if j.installedHere() && j.state().Loaded {
			if clockList(j.Times) == before {
				return nil, nil // nothing changed: leave the job (and a run in progress) alone
			}
			if j.state().Running {
				return nil, badInput("a run is in progress: save the times when it has finished (reinstalling would stop it)")
			}
		}
		return nil, b.installSchedule(st)
	case "schedule/off":
		if st.testDC {
			return nil, badInput("not on Telegram's test servers: the scheduled job runs the real archive")
		}
		j, err := b.launchd(st)
		if err != nil {
			return nil, err
		}
		return nil, j.uninstall()
	case "schedule/check":
		j, err := b.launchd(st)
		if err != nil {
			return nil, err
		}
		if st.testDC || !j.installedHere() {
			return nil, badInput("install the schedule first")
		}
		if b.running(st) {
			// It would start, find run.lock taken and skip without a trace.
			return nil, badInput("a run is in progress: start the check when it has finished")
		}
		return nil, j.kickstart()
	case "restore/snapshots":
		snaps, err := listSnapshots(ctx, st, req.Repo, req.Password)
		return map[string]any{"snapshots": snaps}, err
	case "restore":
		if err := ensureRcloneFor(ctx, st, req.Repo, b.quietWizard(io.Discard)); err != nil {
			return nil, err
		}
		// Not tied to the page: closing the tab must not stop a restore half-way.
		b.dbMu.Lock()
		res, err := restoreArchive(context.WithoutCancel(ctx), st, req.Repo, req.Password, req.Snapshot, req.Dir)
		b.dbMu.Unlock()
		if err != nil {
			return nil, err
		}
		return map[string]any{"snapshot": res.Snapshot.ShortID, "media_added": res.MediaAdded, "media_kept": res.MediaKept,
			"moved_aside": res.MovedAside, "media_replaced": res.MediaReplaced, "media_aside": res.MediaAside, "config_adopted": res.ConfigAdopted, "password_kept": res.PasswordKept}, nil
	}
	return nil, badInput("unknown setup action %q", name)
}

func (b *serveBackend) installSchedule(st setup) error {
	if st.testDC {
		return badInput("not on Telegram's test servers: the scheduled job runs the real archive")
	}
	j, err := b.launchd(st)
	if err != nil {
		return err
	}
	if err := j.install(); err != nil {
		return err
	}
	notifySetup(context.Background(), st, func(err error) { b.logger().Warn("notify", "err", err) })
	return nil
}

// launchd is the scheduled job; tests give one with a fake launchctl.
func (b *serveBackend) launchd(st setup) (launchdJob, error) {
	if b.newJob != nil {
		return b.newJob(st)
	}
	return newLaunchdJob(st, b.env)
}

// saveBackupForm applies the backup card: places and password, then creates
// or checks the repositories. The password comes from the form: shown on the
// page when generated, typed twice otherwise.
func (b *serveBackend) saveBackupForm(ctx context.Context, st setup, enabled bool, local string, extras []string, password string) (any, error) {
	if !enabled {
		return nil, disableBackup(st)
	}
	var repos []string
	if strings.TrimSpace(local) != "" {
		dir, err := checkBackupDir(st, local)
		if err != nil {
			return nil, err
		}
		repos = append(repos, dir)
	}
	for _, e := range extras {
		if e = strings.TrimSpace(e); e != "" {
			repos = append(repos, expandTilde(e, st.paths.Home))
		}
	}
	if err := saveBackup(ctx, st, repos, password); err != nil {
		return nil, err
	}
	var log bytes.Buffer
	out := io.Writer(&log)
	if b.env.Stderr != nil {
		out = io.MultiWriter(&log, b.env.Stderr) // also where serve was started
	}
	err := b.quietWizard(out).createRepos(ctx)
	return map[string]string{"log": strings.TrimSpace(log.String())}, err
}

// chooseFolder shows the macOS folder dialog: a browser does not give the page
// a folder's path.
func chooseFolder(ctx context.Context) (any, error) {
	if runtime.GOOS != "darwin" {
		return nil, badInput("type the path: the folder dialog is only on macOS")
	}
	script := `tell application "System Events"
activate
set f to choose folder with prompt "Choose a folder for tgbup"
end tell
POSIX path of f`
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "-128") { // Cancel in the dialog
			return map[string]string{"path": ""}, nil
		}
		return nil, fmt.Errorf("the folder dialog did not open (%s): allow tgbup to control System Events in System Settings → Privacy & Security → Automation, or type the path",
			strings.TrimSpace(stderr.String()))
	}
	return map[string]string{"path": strings.TrimRight(strings.TrimSpace(string(out)), "/")}, nil
}

// authFlow is the part of gotd's auth client a web sign-in uses.
type authFlow interface {
	SendCode(ctx context.Context, phone string, options auth.SendCodeOptions) (tg.AuthSentCodeClass, error)
	SignIn(ctx context.Context, phone, code, codeHash string) (*tg.AuthAuthorization, error)
	Password(ctx context.Context, password string) (*tg.AuthAuthorization, error)
}

// webLogin is a Telegram sign-in in progress: the connection stays open
// between the page's requests (phone, code, 2FA password).
type webLogin struct {
	auth     authFlow
	close    func()
	phone    string
	hash     string
	password bool
}

// loginIdle closes a sign-in the page left half-way.
const loginIdle = 10 * time.Minute

// dialTelegram opens a connection for a sign-in and returns its auth client.
func dialTelegram(ctx context.Context, st setup) (authFlow, func(), error) {
	runCtx, stop := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	done := make(chan struct{})
	var client *telegram.Client
	go func() {
		defer close(done)
		err := tgclient.Run(runCtx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
			client = c
			ready <- nil
			<-ctx.Done()
			return nil
		})
		select {
		case ready <- err:
		default:
		}
	}()
	closeConn := sync.OnceFunc(func() { stop(); <-done })
	select {
	case err := <-ready:
		if err != nil {
			closeConn()
			return nil, nil, err
		}
	case <-ctx.Done():
		closeConn()
		return nil, nil, ctx.Err()
	}
	return client.Auth(), closeConn, nil
}

func (b *serveBackend) loginSendCode(ctx context.Context, st setup, phone string) error {
	phone = strings.Join(strings.Fields(phone), "")
	if !strings.HasPrefix(phone, "+") || len(phone) < 8 {
		return badInput("enter the phone number in the international format, e.g. +79990001122")
	}
	b.dropLogin(nil)
	dial := b.dialLogin
	if dial == nil {
		dial = dialTelegram
	}
	flow, closeConn, err := dial(ctx, st)
	if err != nil {
		return err
	}
	sent, err := flow.SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		closeConn()
		return loginError(err)
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		closeConn()
		return fmt.Errorf("unexpected answer %T to the login code request", sent)
	}
	l := &webLogin{auth: flow, close: closeConn, phone: phone, hash: code.PhoneCodeHash}
	b.mu.Lock()
	b.login = l
	b.mu.Unlock()
	time.AfterFunc(loginIdle, func() { b.dropLogin(l) })
	return nil
}

// dropLogin closes the sign-in in progress (only l, when given).
func (b *serveBackend) dropLogin(l *webLogin) {
	b.mu.Lock()
	cur := b.login
	if cur != nil && (l == nil || cur == l) {
		b.login = nil
	} else {
		cur = nil
	}
	b.mu.Unlock()
	if cur != nil {
		cur.close()
	}
}

func (b *serveBackend) currentLogin() (*webLogin, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.login == nil || b.login.hash == "" {
		return nil, badInput("send the login code first")
	}
	return b.login, nil
}

func (b *serveBackend) loginSignIn(ctx context.Context, code string) (any, error) {
	l, err := b.currentLogin()
	if err != nil {
		return nil, err
	}
	_, err = l.auth.SignIn(ctx, l.phone, strings.TrimSpace(code), l.hash)
	if errors.Is(err, auth.ErrPasswordAuthNeeded) {
		b.mu.Lock()
		l.password = true
		b.mu.Unlock()
		return map[string]bool{"password_needed": true}, nil
	}
	if err != nil {
		return nil, loginError(err)
	}
	b.finishLogin(l)
	return map[string]bool{"done": true}, nil
}

func (b *serveBackend) loginPassword(ctx context.Context, password string) (any, error) {
	l, err := b.currentLogin()
	if err != nil {
		return nil, err
	}
	if !l.password {
		return nil, badInput("enter the login code first")
	}
	if _, err := l.auth.Password(ctx, password); err != nil {
		return nil, loginError(err)
	}
	b.finishLogin(l)
	return map[string]bool{"done": true}, nil
}

// finishLogin closes the sign-in connection once the session is saved; the
// chat page then connects with it.
func (b *serveBackend) finishLogin(l *webLogin) {
	b.mu.Lock()
	b.loggedIn = true
	b.dialogs = nil
	b.mu.Unlock()
	b.dropLogin(l)
}

// loginError names what went wrong in words the person can act on.
func loginError(err error) error {
	var signUp *auth.SignUpRequired
	switch {
	case errors.As(err, &signUp):
		return badInput("there is no Telegram account with this number: sign up in a Telegram app first")
	case tgerr.Is(err, "PASSWORD_HASH_INVALID"):
		return badInput("wrong password")
	case tgerr.Is(err, "PHONE_NUMBER_INVALID"):
		return badInput("that phone number is not valid: use the international format, e.g. +79990001122")
	case tgerr.Is(err, "PHONE_CODE_INVALID", "PHONE_CODE_EMPTY"):
		return badInput("wrong code")
	case tgerr.Is(err, "PHONE_CODE_EXPIRED"):
		return badInput("the code expired: send a new one")
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return badInput("Telegram asks to wait %s before trying again", d.Round(time.Second))
	}
	if strings.Contains(err.Error(), "API_ID") {
		return badInput("Telegram does not accept the API keys: check api_id and api_hash")
	}
	return err
}
