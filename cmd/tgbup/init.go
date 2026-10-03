package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// errDeclined ends an optional step the person does not want; unlike a
// skipped one it is not reported as missing.
var errDeclined = errors.New("declined")

// initStep is one stage of tgbup init. A step whose check says done is shown
// and passed over, so running init again continues where it stopped.
type initStep struct {
	name string
	// check is nil for steps that always run (they are quick and idempotent).
	check func(ctx context.Context) (status string, done bool, err error)
	// change, when set, offers to redo a done step.
	change string
	// after, when set, runs for a done step that is not redone (a quick check).
	after    func(ctx context.Context) error
	run      func(ctx context.Context) error
	optional bool
}

type wizard struct {
	env Env
	in  *lineReader
	out io.Writer
	// initRepos creates or checks the backup repositories; tests replace it.
	initRepos func(context.Context) error
	// assumeYes answers every yes/no question with yes and gives rclone no
	// terminal: the web setup runs steps this way after the person chose.
	assumeYes bool
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
	if w.assumeYes {
		return true, nil
	}
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
	w.initRepos = w.createRepos
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
		case errors.Is(err, errDeclined):
			fmt.Fprintln(w.out, "  – off (tgbup init can turn it on later)")
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
			redo := false
			if s.change != "" {
				if redo, err = w.yes(ctx, "  "+s.change, false); err != nil {
					return err
				}
			}
			if !redo {
				if s.after != nil {
					return s.after(ctx)
				}
				return nil
			}
		}
	}
	return s.run(ctx)
}

func (w *wizard) steps() []initStep {
	return []initStep{
		{name: "Telegram API keys", check: w.checkKeys, change: "Enter different API keys?", run: w.setKeys},
		{name: "Telegram login", check: w.checkLogin, run: w.login},
		{name: "Archive folder", check: w.checkArchiveDir, run: w.setArchiveDir},
		{name: "Chats to archive", check: w.checkChats, change: "Change the chat list?", run: w.chooseChats},
		{name: "Backups", check: w.checkStorage, change: "Change the backup settings?",
			run: w.setBackup, after: w.initRepos, optional: true},
		{name: "Alerts bot (optional)", check: w.checkBot, run: w.setBot, optional: true},
		{name: "First download", check: w.checkArchive, run: w.firstSync},
		// Optional so that a failed check run reports and still lets init finish.
		{name: "Schedule", check: w.checkSchedule, run: w.schedule, optional: true},
		{name: "Command in PATH", check: w.checkPath, run: w.addPath, optional: true},
	}
}

func (w *wizard) checkKeys(context.Context) (string, bool, error) {
	st, err := w.load()
	if errors.Is(err, config.ErrBadAPIID) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if st.secrets.APIID == 0 || st.secrets.APIHash == "" {
		return "", false, nil
	}
	return fmt.Sprintf("set (api_id %d)", st.secrets.APIID), true, nil
}

func (w *wizard) setKeys(ctx context.Context) error {
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
	id, err := w.askValid(ctx, "  api_id: ", checkAPIID)
	if err != nil {
		return err
	}
	hash, err := w.askValid(ctx, "  api_hash: ", checkAPIHash)
	if err != nil {
		return err
	}
	path, err := saveKeys(w.env, id, hash)
	if err != nil {
		return err
	}
	fmt.Fprintf(w.out, "  saved to %s\n", path)
	return nil
}

// askValid asks until check accepts the answer.
func (w *wizard) askValid(ctx context.Context, prompt string, check func(string) error) (string, error) {
	for {
		v, ok := w.ask(ctx, prompt)
		if !ok {
			return "", errStopped
		}
		if err := check(v); err != nil {
			fmt.Fprintf(w.out, "  %v\n", err)
			continue
		}
		return v, nil
	}
}

func (w *wizard) checkLogin(ctx context.Context) (string, bool, error) {
	st, err := loadSetup(w.env, false)
	if err != nil {
		return "", false, err
	}
	ok, err := telegramAuthorized(ctx, st)
	if err != nil {
		if strings.Contains(err.Error(), "API_ID") {
			return "", false, fmt.Errorf("the API keys are not accepted (%w): run tgbup init and enter them again", err)
		}
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
	return loginTelegram(ctx, st, w.in.buffered(), w.out)
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
	restore, err := w.yes(ctx, "  Restore an archive from an existing backup instead of starting a new one?", false)
	if err != nil {
		return err
	}
	def := tildePath(st.cfg.ArchiveDir, st.paths.Home) // the default until a config sets one
	dir, err := w.askValid(ctx, fmt.Sprintf("  Where to keep the archive? [%s]: ", def), func(v string) error {
		if v == "" {
			return nil
		}
		return checkArchiveDir(v)
	})
	if err != nil {
		return err
	}
	if dir == "" {
		dir = def
	}
	if restore {
		return w.restore(ctx, st, dir)
	}
	if err := saveArchiveDir(st, dir); err != nil {
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

// choosePassword gets a backup password the person has seen: generated and
// shown, or typed. Keeping it is up to them.
func (w *wizard) choosePassword(ctx context.Context) (string, error) {
	if w.assumeYes {
		return "", errors.New("the backup password needs a person to see or type it")
	}
	fmt.Fprintln(w.out, `  Backups are encrypted with a password. Without it they cannot be read back,
  so keep a copy outside this Mac.`)
	fmt.Fprintln(w.out, "  Restoring, or the repositories already exist? Answer n and type the password they were made with.")
	gen, err := w.yes(ctx, "  Generate a new strong password?", true)
	if err != nil {
		return "", err
	}
	if !gen {
		return w.ownPassword(ctx)
	}
	for {
		pass := newPassword()
		ok, err := w.showPassword(ctx, pass)
		if err != nil || ok {
			return pass, err
		}
	}
}

// showPassword shows a generated password and asks whether to use it.
func (w *wizard) showPassword(ctx context.Context, pass string) (bool, error) {
	copied := isTerminal(w.out) && copyToClipboard(pass) == nil
	note := ""
	if copied {
		note = " (copied to the clipboard)"
		// Universal Clipboard would otherwise keep it on every Apple device.
		defer func() { _ = copyToClipboard("") }()
	}
	fmt.Fprintf(w.out, "\n  Backup password%s:\n\n      %s\n\n", note, pass)
	return w.yes(ctx, "  Use this password? (n: another one)", true)
}

// readSecret reads a line without echo on a terminal.
func (w *wizard) readSecret(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(w.out, prompt)
	if f, ok := w.env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return w.readHidden(ctx, int(f.Fd()))
	}
	s, ok := w.in.next(ctx)
	if !ok {
		return "", errStopped
	}
	return s, nil
}

func (w *wizard) ownPassword(ctx context.Context) (string, error) {
	read := func(prompt string) (string, error) { return w.readSecret(ctx, prompt) }
	for {
		p1, err := read("  Password (at least 12 characters, not shown): ")
		if err != nil {
			return "", err
		}
		switch {
		case len([]rune(p1)) < 12:
			fmt.Fprintln(w.out, "  too short")
			continue
		case !config.SecretRoundTrips(p1) || strings.ContainsAny(p1, "\r\n"):
			fmt.Fprintln(w.out, "  it must not start or end with a space or a quote")
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

// readHidden reads a line without echo; Ctrl+C gives up the read and puts
// the terminal back, which term.ReadPassword alone would not do.
func (w *wizard) readHidden(ctx context.Context, fd int) (string, error) {
	saved, _ := term.GetState(fd)
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		ch <- result{string(b), err}
	}()
	select {
	case r := <-ch:
		fmt.Fprintln(w.out)
		return r.s, r.err
	case <-ctx.Done():
		if saved != nil {
			_ = term.Restore(fd, saved)
		}
		// The abandoned read still owns stdin.
		w.in.stopped = true
		fmt.Fprintln(w.out)
		return "", errStopped
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
	// Places without a password: a restored config on a new Mac.
	if len(st.cfg.Backup.Repositories) == 0 || st.secrets.ResticPassword == "" {
		return "", false, nil
	}
	var names []string
	for _, r := range st.cfg.Backup.Repositories {
		// A folder repository shows whether it was created; remote ones are
		// checked when used.
		if filepath.IsAbs(r) && !strings.HasPrefix(r, "/Volumes/") && !isFile(filepath.Join(r, "config")) {
			return tildePath(r, st.paths.Home) + " is not created yet", false, nil
		}
		names = append(names, tildePath(r, st.paths.Home))
	}
	return strings.Join(names, "\n    "), true, nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// defaultBackupDir keeps the first backup on this Mac, outside the archive.
const defaultBackupDir = "~/TelegramBackup"

// backupDirDefault is defaultBackupDir, or a folder next to the test config
// on Telegram's test servers.
func backupDirDefault(st setup) string {
	if st.testDC {
		return tildePath(filepath.Join(filepath.Dir(st.paths.Config), "TelegramBackup"), st.paths.Home)
	}
	return defaultBackupDir
}

// setBackup sets up restic backups: a folder on this Mac by default, plus any
// other places; every run snapshots into all of them.
func (w *wizard) setBackup(ctx context.Context) error {
	st, err := w.load()
	if err != nil {
		return err
	}
	home := st.paths.Home
	current := st.cfg.Backup.Repositories
	if len(current) == 0 {
		fmt.Fprintln(w.out, `  Backups keep earlier versions of the archive (encrypted restic snapshots after every run),
  so a deleted chat, a damaged file or a mistake can be undone. By default they go to a
  folder on this Mac; iCloud, Google Drive or an external disk can be added.`)
		if ok, err := w.yes(ctx, "  Back up the archive?", true); err != nil {
			return err
		} else if !ok {
			return errDeclined
		}
	}
	if len(current) > 0 {
		if keep, err := w.yes(ctx, "  Keep backing up?", true); err != nil {
			return err
		} else if !keep {
			if err := disableBackup(st); err != nil {
				return err
			}
			fmt.Fprintln(w.out, "  backups are off; the snapshots made so far stay where they are")
			return errDeclined
		}
	}
	local, extras := splitRepos(current, home)
	dir, err := w.askBackupDir(ctx, st, local)
	if err != nil {
		return err
	}
	if extras, err = w.chooseExtras(ctx, home, extras); err != nil {
		return err
	}
	var repos []string
	if dir != "" {
		repos = append(repos, dir)
	}
	repos = append(repos, extras...)
	if len(repos) == 0 {
		return errors.New("no backup place chosen")
	}

	// The password is always chosen and saved by the person: whatever the
	// places, a lost password makes every copy unreadable.
	var password string
	switch {
	case st.secrets.ResticPassword == "":
		if password, err = w.choosePassword(ctx); err != nil {
			return err
		}
	case len(current) > 0:
		if ok, err := w.yes(ctx, "  Replace the backup password? (only when your repositories were made with another one)", false); err != nil {
			return err
		} else if ok {
			if password, err = w.choosePassword(ctx); err != nil {
				return err
			}
		}
	}
	if err := saveBackup(ctx, st, repos, password); err != nil {
		return err
	}
	for _, r := range repos {
		fmt.Fprintf(w.out, "  → %s\n", tildePath(r, home))
	}
	return w.initRepos(ctx)
}

// askBackupDir asks for the folder on this Mac; "none" keeps no copy here.
func (w *wizard) askBackupDir(ctx context.Context, st setup, current string) (string, error) {
	home := st.paths.Home
	def := backupDirDefault(st)
	if current != "" {
		def = tildePath(current, home)
	}
	for {
		ans, ok := w.ask(ctx, fmt.Sprintf("  Backup folder on this Mac [%s] (none: no copy on this Mac): ", def))
		if !ok {
			return "", errStopped
		}
		switch strings.ToLower(ans) {
		case "":
			ans = def
		case "none", "нет":
			return "", nil
		}
		dir, err := checkBackupDir(st, ans)
		if err != nil {
			fmt.Fprintf(w.out, "  %v\n", err)
			continue
		}
		return dir, nil
	}
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func expandTilde(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// offDisk reports a backup place that survives this Mac's disk: a cloud
// folder, an external volume or any remote restic repository.
func offDisk(repo, home string) bool {
	p := expandTilde(repo, home)
	if !filepath.IsAbs(p) {
		return true // sftp:, s3:, rclone:…
	}
	for _, dir := range []string{"/Volumes", filepath.Join(home, "Library", "Mobile Documents"), filepath.Join(home, "Library", "CloudStorage")} {
		if within(p, dir) {
			return true
		}
	}
	return false
}

// splitRepos separates the first folder on this Mac from the other places.
func splitRepos(repos []string, home string) (local string, others []string) {
	for _, r := range repos {
		if local == "" && !offDisk(r, home) {
			local = r
			continue
		}
		others = append(others, r)
	}
	return local, others
}

// storageChoice is one more place backups can go.
type storageChoice struct {
	Label string `json:"label"`
	Repo  string `json:"repo"` // "" asks for a path
}

const repoDirName = "TelegramBackup"

// storageChoices lists the cloud folders and external disks found on this
// Mac, Google Drive through rclone, and a free-form entry.
func storageChoices(home, volumes string) []storageChoice {
	var out []storageChoice
	icloud := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	if isDir(icloud) {
		out = append(out, storageChoice{Label: "iCloud Drive", Repo: filepath.Join(icloud, repoDirName)})
	}
	out = append(out, storageChoice{Label: "Google Drive, connected by rclone (sign in once in the browser; needs no Google Drive app)",
		Repo: "rclone:gdrive:" + repoDirName})
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
	vols, _ := os.ReadDir(volumes)
	for _, e := range vols {
		// The startup disk shows up in /Volumes as a symlink to /; Time Machine
		// and recovery volumes are not for this, and a read-only disk cannot be.
		name := e.Name()
		if e.Type()&os.ModeSymlink != 0 || !e.IsDir() || strings.HasPrefix(name, ".") ||
			strings.HasPrefix(name, "com.apple.") || name == "Recovery" ||
			syscall.Access(filepath.Join(volumes, name), 2 /* W_OK */) != nil {
			continue
		}
		out = append(out, storageChoice{Label: "external disk " + e.Name(), Repo: filepath.Join(volumes, e.Name(), repoDirName)})
	}
	return append(out, storageChoice{Label: "another folder or restic repository (sftp:, s3:, rclone:…)"})
}

// chooseExtras asks for the places besides the folder on this Mac; Enter
// keeps the current ones.
func (w *wizard) chooseExtras(ctx context.Context, home string, current []string) ([]string, error) {
	choices := storageChoices(home, "/Volumes")
	fmt.Fprintln(w.out, "  Also back up to (every run snapshots into each place):")
	for i, c := range choices {
		mark := " "
		if c.Repo != "" && slices.ContainsFunc(current, func(r string) bool { return expandTilde(r, home) == c.Repo }) {
			mark = "*"
		}
		fmt.Fprintf(w.out, "  %s %d) %s\n", mark, i+1, c.Label)
	}
	hint := "nothing more"
	if len(current) > 0 {
		hint = "keep the current ones"
	}
	for {
		ans, ok := w.ask(ctx, fmt.Sprintf("  Choose, e.g. 1,2 [Enter = %s, 0 = none]: ", hint))
		if !ok {
			return nil, errStopped
		}
		switch ans {
		case "":
			return current, nil
		case "0":
			return nil, nil
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
					return nil, errStopped
				}
				if repo == "" {
					continue
				}
				repo = expandTilde(repo, home)
			}
			repos = append(repos, repo)
		}
		return repos, nil
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

func (w *wizard) createRepos(ctx context.Context) error {
	// Repositories need no chats: the web setup may set backups up first.
	st, err := loadLocalSetup(w.env, false)
	if err != nil {
		return err
	}
	fmt.Fprintln(w.out, "  getting restic (and rclone when needed): downloaded once, checksums verified…")
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
		return fmt.Errorf("could not open or create %s. If a repository already exists from an earlier setup, "+
			"run tgbup init again, change the backup settings and replace the password with the one it was made with", strings.Join(failed, ", "))
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

// ensureRcloneRemote makes sure the rclone remote exists and works,
// connecting Google Drive (or reconnecting it) through the browser.
func (w *wizard) ensureRcloneRemote(ctx context.Context, o backup.Options, remote string) error {
	rclone := func(ctx context.Context, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, o.Rclone, args...)
		cmd.Env = os.Environ()
		if o.RcloneConfigPass != "" {
			cmd.Env = append(cmd.Env, "RCLONE_CONFIG_PASS="+o.RcloneConfigPass)
		}
		return cmd
	}
	// interactive lets rclone show its sign-in link and ask what it needs.
	// interactive runs rclone with the terminal, so its questions and sign-in
	// link reach the person. config create ends by printing the new remote,
	// Google's token included, so its stdout is never shown (showOut false).
	interactive := func(showOut bool, args ...string) error {
		cmd := rclone(ctx, args...)
		cmd.Stdout, cmd.Stderr = io.Discard, w.env.Stderr
		if showOut && !w.assumeYes {
			cmd.Stdout = w.out
		}
		if !w.assumeYes {
			cmd.Stdin = w.env.Stdin
		}
		return cmd.Run()
	}
	listed := func() (bool, error) {
		out, err := rclone(ctx, "listremotes").Output()
		if err != nil {
			return false, fmt.Errorf("rclone listremotes: %w", err)
		}
		return slices.Contains(strings.Fields(string(out)), remote+":"), nil
	}
	works := func() error {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		out, err := rclone(c, "lsd", remote+":").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %s", remote, lastLine(string(out)))
		}
		return nil
	}

	ok, err := listed()
	if err != nil {
		return err
	}
	if ok {
		err := works()
		if err == nil {
			return nil
		}
		fmt.Fprintf(w.out, "  rclone cannot reach %s: %v\n", remote, err)
		if remote != "gdrive" {
			return fmt.Errorf("fix the rclone remote %q with %s config", remote, o.Rclone)
		}
		if ok, err := w.yes(ctx, "  Sign in to Google Drive again? A browser window opens.", true); err != nil {
			return err
		} else if !ok {
			return errors.New("no access to Google Drive: sign in again or choose other storage in tgbup init")
		}
		if err := interactive(true, "config", "reconnect", remote+":"); err != nil {
			return fmt.Errorf("reconnect Google Drive: %w", err)
		}
	} else {
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
		if err := interactive(false, "config", "create", remote, "drive", "scope=drive.file"); err != nil {
			return fmt.Errorf("connect Google Drive: %w", err)
		}
		if ok, err := listed(); err != nil || !ok {
			return errors.New("the connection to Google Drive was not saved")
		}
	}
	if err := works(); err != nil {
		return fmt.Errorf("still no access to Google Drive: %w", err)
	}
	fmt.Fprintln(w.out, "  Google Drive connected")
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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
			return errDeclined
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
	if err := saveBot(ctx, st, bot); err != nil {
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
	log := newLogger(w.env.Stderr)
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
	if st.testDC {
		// The job is one per Mac and runs the real archive.
		return "left alone on Telegram's test servers (TGBUP_TEST_DC=1)", true, nil
	}
	j, err := newLaunchdJob(st.paths, w.env)
	if err != nil {
		return "", false, err
	}
	if !j.installedHere() || !j.state().Loaded {
		return "", false, nil
	}
	status := "runs at " + strings.Join(runTimes(), ", ")
	// launchd forgets exit codes on reboot; the archive keeps every run.
	switch r, ok, err := lastRun(ctx, st.cfg.ArchiveDir); {
	case err != nil:
		return "", false, err
	case !ok:
		status += "; no run yet"
	case r.OK:
		status += "; the last run worked"
	default:
		status += "; the last run had errors (tgbup serve or the alerts show them)"
	}
	return status, true, nil
}

// lastRun returns the latest tgbup run recorded in the archive.
func lastRun(ctx context.Context, archiveDir string) (store.Run, bool, error) {
	path := filepath.Join(archiveDir, "archive.sqlite")
	if _, err := os.Stat(path); err != nil {
		return store.Run{}, false, nil
	}
	db, err := store.Open(path)
	if err != nil {
		return store.Run{}, false, err
	}
	defer db.Close()
	runs, err := db.RunsSince(ctx, time.Time{})
	if err != nil || len(runs) == 0 {
		return store.Run{}, false, err
	}
	return runs[len(runs)-1], true, nil
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
		st, err := w.load()
		if err != nil {
			return err
		}
		what := "downloads what is new"
		if len(st.cfg.Backup.Repositories) > 0 {
			what += ", backs up"
		}
		if st.secrets.BotToken != "" {
			what += " and alerts on errors"
		}
		fmt.Fprintf(w.out, "  tgbup run %s, at %s.\n", what, strings.Join(runTimes(), ", "))
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
	fmt.Fprintln(w.out, "  A first run in the background checks that it all works unattended.")
	fmt.Fprintln(w.out, "  macOS may ask to let tgbup access iCloud or other folders: allow it.")
	if ok, err := w.yes(ctx, "  Start it now?", true); err != nil {
		return err
	} else if !ok {
		return errSkipped
	}
	st, err := w.load()
	if err != nil {
		return err
	}
	return w.watchRun(ctx, j, st.cfg.ArchiveDir, 2*time.Second, 30*time.Minute)
}

// watchRun starts the job, waits for it to end and reports the run it
// recorded in the archive.
func (w *wizard) watchRun(ctx context.Context, j launchdJob, archiveDir string, every, limit time.Duration) error {
	// Runs are stored with second precision.
	start := time.Now().Truncate(time.Second)
	if err := j.kickstart(); err != nil {
		return err
	}
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
			fmt.Fprintf(w.out, "\n  still running after %s; tgbup init checks the result next time\n", limit)
			return errSkipped
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
	r, found, err := lastRun(ctx, archiveDir)
	if err != nil {
		return err
	}
	if found && !r.Started.Before(start) {
		if r.OK {
			fmt.Fprintln(w.out, "  ✓ the scheduled run worked")
			return nil
		}
		var rec runRecord
		_ = json.Unmarshal([]byte(r.Summary), &rec)
		fmt.Fprintln(w.out, "  the scheduled run had errors:")
		for _, e := range append(rec.SyncErrors, rec.BackupErrors...) {
			fmt.Fprintf(w.out, "    %s\n", e)
		}
	} else {
		// It ended before recording a run: a broken setup, a crash or a signal.
		why := "ended without a result"
		if code := j.state().LastExit; code != "" && code != "0" {
			why = "failed with exit code " + code
		}
		fmt.Fprintf(w.out, "  the scheduled run %s\n", why)
	}
	tail := lastLogLines(j.LogPath, 15)
	fmt.Fprintf(w.out, "  End of %s:\n%s\n", j.LogPath, indent(tail, "    "))
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
	if w.env.Getenv("TGBUP_TEST_DC") == "1" {
		return "left alone on Telegram's test servers (TGBUP_TEST_DC=1)", true, nil
	}
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
	fmt.Fprintln(w.out, "  web page:  tgbup serve --open (status, update now, chat list)")
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
