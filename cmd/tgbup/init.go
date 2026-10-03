package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/notify"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

// errSkipped ends a step the person chose to leave for later.
var errSkipped = errors.New("skipped")

// initStep is one stage of tgbup init. A step whose check says done is shown
// and passed over, so running init again continues where it stopped.
type initStep struct {
	name string
	// check is nil for steps that always run (they are quick and idempotent).
	check func(ctx context.Context) (status string, done bool, err error)
	// change, when set, offers to redo a done step.
	change   string
	run      func(ctx context.Context) error
	optional bool
}

type wizard struct {
	env Env
	in  *lineReader
	out io.Writer
}

func (w *wizard) ask(ctx context.Context, prompt string) (string, bool) {
	fmt.Fprint(w.out, prompt)
	line, ok := w.in.next(ctx)
	if !ok {
		fmt.Fprintln(w.out)
	}
	return strings.TrimSpace(line), ok
}

// yes asks a yes/no question; Enter takes def. It fails only when input ends.
func (w *wizard) yes(ctx context.Context, prompt string, def bool) (bool, error) {
	hint := " [y/N]: "
	if def {
		hint = " [Y/n]: "
	}
	for {
		ans, ok := w.ask(ctx, prompt+hint)
		if !ok {
			return false, errStopped
		}
		switch strings.ToLower(ans) {
		case "":
			return def, nil
		case "y", "yes", "д", "да", "н": // н is y in the Russian layout
			return true, nil
		case "n", "no", "нет", "т":
			return false, nil
		}
	}
}

var errStopped = errors.New("stopped")

func (w *wizard) load() (setup, error) { return loadLocalSetup(w.env, false) }

func cmdInit(ctx context.Context, env Env) int {
	w := &wizard{env: env, out: env.Stdout,
		in: newLineReader(env.Stdin, func(err error) { fmt.Fprintln(env.Stderr, "read input:", err) })}
	fmt.Fprintln(w.out, "tgbup init sets everything up step by step. Finished steps are kept:")
	fmt.Fprintln(w.out, "stop any time (Ctrl+C) and run tgbup init again to continue.")
	skipped, ok := runSteps(ctx, w, w.steps())
	if !ok {
		return 1
	}
	w.finish(ctx, skipped)
	return 0
}

// runSteps reports whether every required step is done, and which steps were
// left for later.
func runSteps(ctx context.Context, w *wizard, steps []initStep) (skipped []string, ok bool) {
	for i, s := range steps {
		fmt.Fprintf(w.out, "\n[%d/%d] %s\n", i+1, len(steps), s.name)
		err := runStep(ctx, w, s)
		switch {
		case err == nil:
		case errors.Is(err, errSkipped):
			fmt.Fprintln(w.out, "  – skipped: tgbup init offers it again next time")
			skipped = append(skipped, s.name)
		case errors.Is(err, errStopped) || ctx.Err() != nil:
			fmt.Fprintln(w.out, "\nStopped. Run tgbup init again to continue from this step.")
			return skipped, false
		case s.optional:
			fmt.Fprintf(w.out, "  ✗ %v\n  (optional: tgbup init offers it again next time)\n", err)
			skipped = append(skipped, s.name)
		default:
			fmt.Fprintf(w.out, "  ✗ %v\n\nFix this and run tgbup init again: finished steps are kept.\n", err)
			return skipped, false
		}
	}
	return skipped, true
}

func runStep(ctx context.Context, w *wizard, s initStep) error {
	if s.check != nil {
		status, done, err := s.check(ctx)
		if err != nil {
			return err
		}
		if done {
			fmt.Fprintf(w.out, "  ✓ %s\n", status)
			if s.change == "" {
				return nil
			}
			redo, err := w.yes(ctx, "  "+s.change, false)
			if err != nil || !redo {
				return err
			}
		}
	}
	return s.run(ctx)
}

func (w *wizard) steps() []initStep {
	return []initStep{
		{name: "Telegram API keys", check: w.checkKeys, run: w.setKeys},
		{name: "Telegram login", check: w.checkLogin, run: w.login},
		{name: "Archive folder", check: w.checkArchiveDir, run: w.setArchiveDir},
		{name: "Chats to archive", check: w.checkChats, change: "Change the chat list?", run: w.chooseChats},
		{name: "Backup password", check: w.checkPassword, run: w.setPassword},
		{name: "Backup storage", check: w.checkStorage, change: "Change where backups go?", run: w.chooseStorage},
		{name: "Backup repositories", run: w.initRepos},
		{name: "Alerts bot (optional)", check: w.checkBot, run: w.setBot, optional: true},
		{name: "First download", check: w.checkArchive, run: w.firstSync},
		{name: "Schedule", check: w.checkSchedule, run: w.schedule},
		{name: "Command in PATH", check: w.checkPath, run: w.addPath, optional: true},
	}
}

func (w *wizard) checkKeys(context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	if st.secrets.APIID == 0 || st.secrets.APIHash == "" {
		return "", false, nil
	}
	return fmt.Sprintf("set (api_id %d)", st.secrets.APIID), true, nil
}

var apiHashRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (w *wizard) setKeys(ctx context.Context) error {
	st, err := w.load()
	if err != nil {
		return err
	}
	fmt.Fprintln(w.out, `  Telegram gives every program its own keys:
    1. open https://my.telegram.org and sign in with your phone number
    2. API development tools → create an app (any title, e.g. "tgbup"; platform Desktop)
    3. copy api_id and api_hash below`)
	if w.canOpen() {
		if ok, err := w.yes(ctx, "  Open my.telegram.org in the browser?", true); err != nil {
			return err
		} else if ok {
			openURL("https://my.telegram.org")
		}
	}
	var id, hash string
	for {
		v, ok := w.ask(ctx, "  api_id: ")
		if !ok {
			return errStopped
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			id = v
			break
		}
		fmt.Fprintln(w.out, "  api_id is a number, e.g. 12345678")
	}
	for {
		v, ok := w.ask(ctx, "  api_hash: ")
		if !ok {
			return errStopped
		}
		if v = strings.ToLower(v); apiHashRe.MatchString(v) {
			hash = v
			break
		}
		fmt.Fprintln(w.out, "  api_hash is 32 characters 0-9 and a-f")
	}
	if err := config.SetSecrets(st.paths.Secrets, map[string]string{"TG_API_ID": id, "TG_API_HASH": hash}); err != nil {
		return err
	}
	fmt.Fprintf(w.out, "  saved to %s\n", st.paths.Secrets)
	return nil
}

func (w *wizard) checkLogin(ctx context.Context) (string, bool, error) {
	st, err := loadSetup(w.env, false)
	if err != nil {
		return "", false, err
	}
	ok, err := telegramAuthorized(ctx, st)
	if err != nil {
		return "", false, fmt.Errorf("connect to Telegram: %w", err)
	}
	return "logged in", ok, nil
}

func (w *wizard) login(ctx context.Context) error {
	st, err := loadSetup(w.env, false)
	if err != nil {
		return err
	}
	fmt.Fprintln(w.out, "  Telegram sends a login code to your Telegram app.")
	return loginTelegram(ctx, st, w.in, w.out)
}

func (w *wizard) checkArchiveDir(context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(st.paths.Config); err != nil {
		return "", false, nil
	}
	return tildePath(st.cfg.ArchiveDir, st.paths.Home), true, nil
}

func (w *wizard) setArchiveDir(ctx context.Context) error {
	st, err := w.load()
	if err != nil {
		return err
	}
	def := config.Default().ArchiveDir
	dir, ok := w.ask(ctx, fmt.Sprintf("  Where to keep the archive? [%s]: ", def))
	if !ok {
		return errStopped
	}
	if dir == "" {
		dir = def
	}
	if !strings.HasPrefix(dir, "~") && !filepath.IsAbs(dir) {
		return fmt.Errorf("%q: give a full path or one starting with ~/", dir)
	}
	if err := config.SetValue(st.paths.Config, []string{"archive_dir"}, dir); err != nil {
		return err
	}
	fmt.Fprintf(w.out, "  config: %s\n", st.paths.Config)
	return nil
}

func (w *wizard) checkChats(context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	t := st.cfg.Targets
	if len(t.Include) == 0 && len(t.Folders) == 0 {
		return "", false, nil
	}
	status := plural(len(t.Include), "chat")
	if len(t.Folders) > 0 {
		status += fmt.Sprintf(" and folders %s", strings.Join(t.Folders, ", "))
	}
	return status, true, nil
}

func (w *wizard) chooseChats(ctx context.Context) error {
	st, err := loadSetup(w.env, false)
	if err != nil {
		return err
	}
	saved, err := chooseChats(ctx, st, w.in, w.out, w.env.Stderr)
	if err != nil {
		return err
	}
	if !saved {
		if ctx.Err() != nil {
			return errStopped
		}
		if _, done, _ := w.checkChats(ctx); done {
			return nil
		}
		return errors.New("no chats chosen: the archive needs at least one")
	}
	return nil
}

func (w *wizard) checkPassword(context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	return "set", st.secrets.ResticPassword != "", nil
}

func (w *wizard) setPassword(ctx context.Context) error {
	st, err := w.load()
	if err != nil {
		return err
	}
	fmt.Fprintln(w.out, `  Backups are encrypted with a password. Without it they cannot be read back,
  so it must also be kept outside this Mac, in a password manager.`)
	gen, err := w.yes(ctx, "  Generate a strong password?", true)
	if err != nil {
		return err
	}
	var pass string
	if gen {
		pass = newPassword()
		copied := ""
		if isTerminal(w.out) && copyToClipboard(pass) == nil {
			copied = " (copied to the clipboard)"
		}
		fmt.Fprintf(w.out, "\n  Password%s:\n\n      %s\n\n", copied, pass)
	} else if pass, err = w.ownPassword(ctx); err != nil {
		return err
	}
	for {
		ans, ok := w.ask(ctx, `  Save it in your password manager (e.g. 1Password, item "tgbup backups"), then type "saved": `)
		if !ok {
			return errStopped
		}
		if a := strings.ToLower(ans); a == "saved" || a == "сохранил" || a == "сохранила" {
			break
		}
	}
	if err := config.SetSecrets(st.paths.Secrets, map[string]string{"RESTIC_PASSWORD": pass}); err != nil {
		return err
	}
	fmt.Fprintf(w.out, "  saved to %s\n", st.paths.Secrets)
	return nil
}

func (w *wizard) ownPassword(ctx context.Context) (string, error) {
	read := func(prompt string) (string, error) {
		fmt.Fprint(w.out, prompt)
		if f, ok := w.env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(w.out)
			return string(b), err
		}
		s, ok := w.in.next(ctx)
		if !ok {
			return "", errStopped
		}
		return s, nil
	}
	for {
		p1, err := read("  Password (at least 12 characters, not shown): ")
		if err != nil {
			return "", err
		}
		if len([]rune(p1)) < 12 || strings.ContainsAny(p1, "\r\n") {
			fmt.Fprintln(w.out, "  too short")
			continue
		}
		p2, err := read("  Once more: ")
		if err != nil {
			return "", err
		}
		if p1 == p2 {
			return p1, nil
		}
		fmt.Fprintln(w.out, "  the two do not match")
	}
}

// passwordAlphabet leaves out look-alikes (0/o, 1/l/i) so the password can be typed from paper.
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// newPassword returns 30 random characters (about 148 bits) in groups of five.
func newPassword() string {
	var b strings.Builder
	for i := range 30 {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			panic(err) // crypto/rand does not fail on supported systems
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String()
}

func (w *wizard) checkStorage(context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	if len(st.cfg.Backup.Repositories) == 0 {
		return "", false, nil
	}
	var names []string
	for _, r := range st.cfg.Backup.Repositories {
		names = append(names, tildePath(r, st.paths.Home))
	}
	return strings.Join(names, "\n    "), true, nil
}

// storageChoice is one place backups can go.
type storageChoice struct {
	Label string
	Repo  string // "" asks for a path
	// Default marks the choices preselected when Enter is pressed.
	Default bool
}

const repoDirName = "tgbup-restic"

// storageChoices lists the cloud folders found on this Mac, Google Drive
// through rclone, and a free-form entry.
func storageChoices(home string) []storageChoice {
	var out []storageChoice
	icloud := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	if isDir(icloud) {
		out = append(out, storageChoice{Label: "iCloud Drive", Repo: filepath.Join(icloud, repoDirName), Default: true})
	}
	out = append(out, storageChoice{Label: "Google Drive, connected by rclone (sign in once in the browser; needs no Google Drive app)",
		Repo: "rclone:gdrive:" + repoDirName, Default: true})
	entries, _ := os.ReadDir(filepath.Join(home, "Library", "CloudStorage"))
	for _, e := range entries {
		dir := filepath.Join(home, "Library", "CloudStorage", e.Name())
		if !isDir(dir) {
			continue
		}
		label := e.Name()
		if acct, ok := strings.CutPrefix(e.Name(), "GoogleDrive-"); ok {
			label = "Google Drive app folder (" + acct + ")"
			if isDir(filepath.Join(dir, "My Drive")) {
				dir = filepath.Join(dir, "My Drive")
			}
		}
		out = append(out, storageChoice{Label: label, Repo: filepath.Join(dir, repoDirName)})
	}
	return append(out, storageChoice{Label: "another folder or restic repository (sftp:, s3:, rclone:…)"})
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (w *wizard) chooseStorage(ctx context.Context) error {
	st, err := w.load()
	if err != nil {
		return err
	}
	choices := storageChoices(st.paths.Home)
	fmt.Fprintln(w.out, "  Backups are restic snapshots; two places are safer than one.")
	var defaults []string
	for i, c := range choices {
		mark := " "
		if c.Default {
			mark = "*"
			defaults = append(defaults, strconv.Itoa(i+1))
		}
		fmt.Fprintf(w.out, "  %s %d) %s\n", mark, i+1, c.Label)
	}
	for {
		ans, ok := w.ask(ctx, fmt.Sprintf("  Choose, e.g. 1,2 [Enter = %s]: ", strings.Join(defaults, ",")))
		if !ok {
			return errStopped
		}
		if ans == "" {
			ans = strings.Join(defaults, ",")
		}
		idx, err := parseChoice(ans, len(choices))
		if err != nil {
			fmt.Fprintf(w.out, "  %v\n", err)
			continue
		}
		var repos []string
		for _, i := range idx {
			repo := choices[i].Repo
			if repo == "" {
				if repo, ok = w.ask(ctx, "  Folder or repository: "); !ok {
					return errStopped
				}
				if repo == "" {
					continue
				}
			}
			repos = append(repos, tildePath(repo, st.paths.Home))
		}
		if len(repos) == 0 {
			fmt.Fprintln(w.out, "  choose at least one place")
			continue
		}
		if err := config.SetValue(st.paths.Config, []string{"backup", "repositories"}, repos); err != nil {
			return err
		}
		for _, r := range repos {
			fmt.Fprintf(w.out, "  → %s\n", r)
		}
		return nil
	}
}

// parseChoice reads "1,3" or "1-2" as 0-based indexes.
func parseChoice(s string, n int) ([]int, error) {
	var out []int
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			hi = lo
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a < 1 || b > n || a > b {
			return nil, fmt.Errorf("no choice %q (1-%d)", part, n)
		}
		for i := a; i <= b; i++ {
			if !slices.Contains(out, i-1) {
				out = append(out, i-1)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("choose at least one")
	}
	return out, nil
}

func (w *wizard) initRepos(ctx context.Context) error {
	st, err := loadLocalSetup(w.env, true)
	if err != nil {
		return err
	}
	fmt.Fprintln(w.out, "  getting restic (and rclone) — downloaded once, checksums verified…")
	o, err := backupOptions(ctx, st)
	if err != nil {
		return err
	}
	for _, r := range o.Repos {
		if remote, ok := rcloneRemote(r); ok {
			if err := w.ensureRcloneRemote(ctx, o, remote); err != nil {
				return err
			}
		}
	}
	release, err := acquireLock(st.paths.Lock())
	if err != nil {
		return fmt.Errorf("%w: wait for it to finish and run tgbup init again", err)
	}
	defer release()
	var failed []string
	for _, r := range backup.Init(ctx, o) {
		if r.Err != nil {
			fmt.Fprintf(w.out, "  %s: %v\n", tildePath(r.Repo, st.paths.Home), r.Err)
			failed = append(failed, tildePath(r.Repo, st.paths.Home))
			continue
		}
		fmt.Fprintf(w.out, "  %s: ready\n", tildePath(r.Repo, st.paths.Home))
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not open or create %s (a wrong RESTIC_PASSWORD for an existing repository fails here too)",
			strings.Join(failed, ", "))
	}
	return nil
}

// rcloneRemote returns "gdrive" for "rclone:gdrive:path".
func rcloneRemote(repo string) (string, bool) {
	rest, ok := strings.CutPrefix(repo, "rclone:")
	if !ok {
		return "", false
	}
	remote, _, ok := strings.Cut(rest, ":")
	return remote, ok && remote != ""
}

func (w *wizard) ensureRcloneRemote(ctx context.Context, o backup.Options, remote string) error {
	rclone := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, o.Rclone, args...)
		cmd.Env = os.Environ()
		if o.RcloneConfigPass != "" {
			cmd.Env = append(cmd.Env, "RCLONE_CONFIG_PASS="+o.RcloneConfigPass)
		}
		return cmd
	}
	out, err := rclone("listremotes").Output()
	if err != nil {
		return fmt.Errorf("rclone listremotes: %w", err)
	}
	if slices.Contains(strings.Fields(string(out)), remote+":") {
		return nil
	}
	if remote != "gdrive" {
		return fmt.Errorf("rclone remote %q is not set up: run %s config", remote, o.Rclone)
	}
	fmt.Fprintln(w.out, "  Google Drive is not connected yet. A browser window opens: sign in and allow access.")
	fmt.Fprintln(w.out, "  rclone gets access only to the files it creates itself.")
	if ok, err := w.yes(ctx, "  Connect Google Drive now?", true); err != nil {
		return err
	} else if !ok {
		return errors.New("not connected to Google Drive: choose other storage or run tgbup init again")
	}
	cmd := rclone("config", "create", remote, "drive", "scope=drive.file")
	cmd.Stdout, cmd.Stderr = io.Discard, w.env.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("connect Google Drive: %w", err)
	}
	out, err = rclone("listremotes").Output()
	if err != nil || !slices.Contains(strings.Fields(string(out)), remote+":") {
		return errors.New("the connection to Google Drive was not saved")
	}
	fmt.Fprintln(w.out, "  Google Drive connected")
	return nil
}

func (w *wizard) checkBot(context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	return "set", st.secrets.BotToken != "" && st.secrets.BotChatID != "", nil
}

var botTokenRe = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{30,}$`)

func (w *wizard) setBot(ctx context.Context) error {
	st, err := w.load()
	if err != nil {
		return err
	}
	fmt.Fprintln(w.out, `  Your own Telegram bot reports errors right away and sends a weekly summary.
    1. in Telegram open @BotFather → /newbot → any name and username
    2. paste the token it gives (looks like 123456789:AAE…)`)
	var bot notify.Bot
	var name string
	for {
		token, ok := w.ask(ctx, "  Token (Enter to skip): ")
		if !ok {
			return errStopped
		}
		if token == "" {
			return errSkipped
		}
		if !botTokenRe.MatchString(token) {
			fmt.Fprintln(w.out, "  that does not look like a bot token")
			continue
		}
		bot = notify.Bot{Token: token}
		if name, err = bot.Me(ctx); err != nil {
			fmt.Fprintf(w.out, "  %v\n", err)
			continue
		}
		break
	}
	fmt.Fprintf(w.out, "  Now send any message to your bot: https://t.me/%s (waiting up to 3 minutes)\n", name)
	if w.canOpen() {
		openURL("https://t.me/" + name)
	}
	wait, cancel := context.WithTimeout(ctx, 3*time.Minute)
	chat, err := bot.PrivateChat(wait, 25*time.Second)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return errStopped
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("no message reached the bot in 3 minutes")
		}
		return err
	}
	if ok, err := w.yes(ctx, fmt.Sprintf("  Got a message from %s. Send alerts to this chat?", chat.Name), true); err != nil {
		return err
	} else if !ok {
		return errSkipped
	}
	bot.ChatID = strconv.FormatInt(chat.ID, 10)
	if err := bot.Send(ctx, "✅ tgbup on "+hostname()+": alerts will come here"); err != nil {
		return err
	}
	if err := config.SetSecrets(st.paths.Secrets, map[string]string{"BOT_TOKEN": bot.Token, "BOT_CHAT_ID": bot.ChatID}); err != nil {
		return err
	}
	fmt.Fprintln(w.out, "  sent a test message; saved")
	return nil
}

func (w *wizard) checkArchive(ctx context.Context) (string, bool, error) {
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	n, err := archivedMessages(ctx, st.cfg.ArchiveDir)
	if err != nil || n == 0 {
		return "", false, err
	}
	return fmt.Sprintf("%s in %s", plural(n, "message"), tildePath(st.cfg.ArchiveDir, st.paths.Home)), true, nil
}

func archivedMessages(ctx context.Context, archiveDir string) (int, error) {
	path := filepath.Join(archiveDir, "archive.sqlite")
	if _, err := os.Stat(path); err != nil {
		return 0, nil
	}
	db, err := store.Open(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return db.MessageTotal(ctx)
}

func (w *wizard) firstSync(ctx context.Context) error {
	fmt.Fprintln(w.out, `  The first download takes minutes to hours, depending on the chats.
  Ctrl+C stops it safely; the next run continues from there.`)
	if ok, err := w.yes(ctx, "  Download now?", true); err != nil {
		return err
	} else if !ok {
		return errSkipped
	}
	st, err := loadSetup(w.env, true)
	if err != nil {
		return err
	}
	release, err := acquireLock(st.paths.Lock())
	if err != nil {
		return fmt.Errorf("%w: wait for it to finish and run tgbup init again", err)
	}
	defer release()
	db, err := store.Open(filepath.Join(st.cfg.ArchiveDir, "archive.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(w.env.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Takeout gets higher limits for a full export; Telegram asks to allow it in the app first.
	takeout := true
	for {
		sum, err := syncArchive(ctx, st, db, log, w.env.Stderr, takeout)
		var unconfirmed *tgclient.TakeoutNotConfirmed
		if errors.As(err, &unconfirmed) {
			fmt.Fprintln(w.out, `  Telegram asks to allow a data export: open Telegram, find the message from
  "Telegram" (the service chat) and allow it.`)
			ans, ok := w.ask(ctx, "  Press Enter once allowed (or s to download without it): ")
			if !ok {
				return errStopped
			}
			if strings.EqualFold(ans, "s") {
				takeout = false
			}
			continue
		}
		if err != nil {
			return err
		}
		sum.print(w.out)
		if !sum.ok() {
			fmt.Fprintln(w.out, "  some items failed; scheduled runs try them again")
		}
		return nil
	}
}

func (w *wizard) job() (launchdJob, error) {
	st, err := w.load()
	if err != nil {
		return launchdJob{}, err
	}
	return newLaunchdJob(st.paths, w.env)
}

func (w *wizard) checkSchedule(ctx context.Context) (string, bool, error) {
	if runtime.GOOS != "darwin" {
		return "not on macOS: start `tgbup run` from cron or systemd 4 times a day", true, nil
	}
	st, err := w.load()
	if err != nil {
		return "", false, err
	}
	j, err := newLaunchdJob(st.paths, w.env)
	if err != nil {
		return "", false, err
	}
	if !j.installedHere() || !j.state().Loaded {
		return "", false, nil
	}
	// launchd forgets exit codes on reboot; the archive keeps every run.
	if ok, err := lastRunOK(ctx, st.cfg.ArchiveDir); err != nil || !ok {
		return "", false, err
	}
	return "runs at " + strings.Join(runTimes(), ", ") + "; the last run worked", true, nil
}

// lastRunOK reports whether the latest tgbup run recorded in the archive succeeded.
func lastRunOK(ctx context.Context, archiveDir string) (bool, error) {
	path := filepath.Join(archiveDir, "archive.sqlite")
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	db, err := store.Open(path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	runs, err := db.RunsSince(ctx, time.Time{})
	if err != nil || len(runs) == 0 {
		return false, err
	}
	return runs[len(runs)-1].OK, nil
}

// plural writes "1 chat", "2 chats", "1 234 messages".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return groupDigits(n) + " " + noun + "s"
}

func runTimes() []string {
	out := make([]string, len(runHours))
	for i, h := range runHours {
		out[i] = fmt.Sprintf("%02d:30", h)
	}
	return out
}

func (w *wizard) schedule(ctx context.Context) error {
	j, err := w.job()
	if err != nil {
		return err
	}
	if !j.installedHere() || !j.state().Loaded {
		fmt.Fprintf(w.out, "  tgbup run downloads what is new, backs up and alerts on errors, at %s.\n", strings.Join(runTimes(), ", "))
		fmt.Fprintln(w.out, "  A run missed while the Mac sleeps starts on wake-up.")
		if ok, err := w.yes(ctx, "  Install the schedule?", true); err != nil {
			return err
		} else if !ok {
			return errSkipped
		}
		if err := j.install(); err != nil {
			return err
		}
		fmt.Fprintln(w.out, "  installed")
	}
	fmt.Fprintln(w.out, "  A first run in the background checks that it all works unattended: download,")
	fmt.Fprintln(w.out, "  backups and alerts. macOS may ask to let tgbup access iCloud or other folders: allow it.")
	if ok, err := w.yes(ctx, "  Start it now?", true); err != nil {
		return err
	} else if !ok {
		return errSkipped
	}
	return w.watchRun(ctx, j, 2*time.Second, 30*time.Minute)
}

// watchRun starts the job and waits for it to end.
func (w *wizard) watchRun(ctx context.Context, j launchdJob, every, limit time.Duration) error {
	if err := j.kickstart(); err != nil {
		return err
	}
	start := time.Now()
	seen := false
	tty := isTerminal(w.out)
	for {
		s := j.state()
		elapsed := time.Since(start)
		if s.Running {
			seen = true
		} else if seen || elapsed > 15*every {
			if tty {
				fmt.Fprint(w.out, "\r\033[K")
			}
			break
		}
		if elapsed > limit {
			fmt.Fprintf(w.out, "\n  still running after %s; check later with tgbup schedule status\n", limit)
			return nil
		}
		if tty {
			fmt.Fprintf(w.out, "\r\033[K  running… %s (log: %s)", elapsed.Round(time.Second), j.LogPath)
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(w.out, "\n  stopped waiting; the run goes on in the background")
			return errStopped
		case <-time.After(every):
		}
	}
	s := j.state()
	if s.LastExit == "0" {
		fmt.Fprintln(w.out, "  ✓ the scheduled run worked")
		return nil
	}
	tail := lastLogLines(j.LogPath, 15)
	fmt.Fprintf(w.out, "  the scheduled run failed (exit code %s). End of %s:\n%s\n", s.LastExit, j.LogPath, indent(tail, "    "))
	if strings.Contains(strings.ToLower(tail), "operation not permitted") {
		fmt.Fprintf(w.out, "  macOS blocked file access: System Settings → Privacy & Security → Full Disk Access → + → %s\n", j.Exe)
	}
	return errors.New("the scheduled run failed")
}

func lastLogLines(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func (w *wizard) checkPath(context.Context) (string, bool, error) {
	j, err := w.job()
	if err != nil {
		return "", false, err
	}
	dir := filepath.Dir(j.Exe)
	return "tgbup works in any terminal", slices.Contains(filepath.SplitList(w.env.Getenv("PATH")), dir), nil
}

// profileLine puts dir in PATH for new login shells; $HOME keeps the file portable.
func profileLine(dir, home string) string {
	if rel, ok := strings.CutPrefix(dir, home+string(filepath.Separator)); ok {
		dir = "$HOME/" + rel
	}
	return fmt.Sprintf(`export PATH="%s:$PATH"`, dir)
}

func (w *wizard) addPath(ctx context.Context) error {
	j, err := w.job()
	if err != nil {
		return err
	}
	home := w.env.Getenv("HOME")
	profile := filepath.Join(home, ".zprofile")
	if strings.HasSuffix(w.env.Getenv("SHELL"), "bash") {
		profile = filepath.Join(home, ".bash_profile")
	}
	line := profileLine(filepath.Dir(j.Exe), home)
	if ok, err := w.yes(ctx, fmt.Sprintf("  Add %s to %s so that `tgbup` works in new terminals?", filepath.Dir(j.Exe), tildePath(profile, home)), true); err != nil {
		return err
	} else if !ok {
		return errSkipped
	}
	old, err := os.ReadFile(profile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !strings.Contains(string(old), line) {
		f, err := os.OpenFile(profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		prefix := "\n"
		if len(old) == 0 || strings.HasSuffix(string(old), "\n") {
			prefix = ""
		}
		_, werr := fmt.Fprintf(f, "%s# tgbup\n%s\n", prefix, line)
		if err := errors.Join(werr, f.Close()); err != nil {
			return err
		}
	}
	fmt.Fprintln(w.out, "  done: open a new terminal window for it to take effect")
	return nil
}

func (w *wizard) finish(ctx context.Context, skipped []string) {
	st, err := w.load()
	if err != nil {
		return
	}
	index := filepath.Join(st.cfg.ArchiveDir, "html", "index.html")
	if len(skipped) == 0 {
		fmt.Fprintln(w.out, "\nAll set.")
	} else {
		fmt.Fprintf(w.out, "\nSet up, except: %s (run tgbup init again for them).\n", strings.Join(skipped, ", "))
	}
	fmt.Fprintf(w.out, "  archive:   %s\n", tildePath(st.cfg.ArchiveDir, st.paths.Home))
	if _, err := os.Stat(index); err == nil {
		fmt.Fprintf(w.out, "  read it:   open %s\n", tildePath(index, st.paths.Home))
	}
	fmt.Fprintf(w.out, "  schedule:  tgbup schedule status · log %s\n", tildePath(filepath.Join(st.paths.StateDir, "logs", "run.log"), st.paths.Home))
	fmt.Fprintln(w.out, "  settings:  tgbup init again, or tgbup setup for the chat list")
	if _, err := os.Stat(index); err == nil && w.canOpen() {
		if ok, _ := w.yes(ctx, "Open the archive in the browser?", true); ok {
			openURL(index)
		}
	}
}

// tildePath shows paths under home as ~/…, which the config also accepts.
func tildePath(p, home string) string {
	if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok && home != "" {
		return "~/" + rel
	}
	return p
}

// canOpen reports whether links can be opened for the person at the terminal.
func (w *wizard) canOpen() bool { return runtime.GOOS == "darwin" && isTerminal(w.out) }

func openURL(u string) { _ = exec.Command("open", u).Start() }

func copyToClipboard(s string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("no clipboard")
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}
