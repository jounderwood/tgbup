// Package backup snapshots the archive into restic repositories: a checked,
// consistent copy of the database, the config, media/ and html/, then forget by the
// retention policy, with prune and check on their own, longer intervals.
package backup

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jounderwood/tgbup/internal/config"
)

// Archive is what a backup needs from the archive database.
type Archive interface {
	IntegrityCheck(ctx context.Context) error
	VacuumInto(ctx context.Context, path string) error
	Meta(ctx context.Context, key string) (string, bool, error)
	SetMeta(ctx context.Context, key, value string) error
	LastSnapshot(ctx context.Context, repo string) (id string, update int, ok bool, err error)
	AddSnapshot(ctx context.Context, repo, id string, update int, partial bool, at time.Time) error
	ForgetSnapshots(ctx context.Context, repo string, ids []string, at time.Time) error
}

type Options struct {
	Restic, Rclone             string
	Repos                      []string
	Retention                  config.Retention
	PruneEvery, CheckEvery     time.Duration
	CheckSubset                string
	Password, RcloneConfigPass string
	// WorkDir holds the database copy and the config copy that go into each snapshot.
	WorkDir    string
	ArchiveDir string
	ConfigPath string
	Host       string
	Now        func() time.Time
	// Update is the archive's latest update (store.NoteUpdate). A place whose
	// last snapshot holds it gets no new one: nothing changed. 0, or Force,
	// snapshots every place.
	Update int
	Force  bool
}

type RepoResult struct {
	Repo     string
	Snapshot string
	// Skipped: the place already had a snapshot of this update, Snapshot.
	Skipped bool
	Pruned  bool
	Checked bool
	// Warning: the snapshot was made but some files could not be read.
	Warning string
	Err     error
}

type Report struct {
	// Integrity is set when the archive failed its check; nothing is snapshotted then.
	Integrity error
	Repos     []RepoResult
}

func (r Report) OK() bool {
	if r.Integrity != nil {
		return false
	}
	for _, rr := range r.Repos {
		if rr.Err != nil {
			return false
		}
	}
	return true
}

func metaKey(repo, what string) string { return "backup:" + repo + ":" + what }

// LastSuccess is when the repository last got a snapshot.
func LastSuccess(ctx context.Context, a Archive, repo string) (time.Time, bool) {
	return metaTime(ctx, a, metaKey(repo, "ok_at"))
}

// RecordSuccess notes that the repository got a snapshot at t.
func RecordSuccess(ctx context.Context, a Archive, repo string, t time.Time) error {
	return a.SetMeta(ctx, metaKey(repo, "ok_at"), strconv.FormatInt(t.Unix(), 10))
}

func metaTime(ctx context.Context, a Archive, key string) (time.Time, bool) {
	v, ok, err := a.Meta(ctx, key)
	if err != nil || !ok {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// Init creates the repositories that do not exist yet.
func Init(ctx context.Context, o Options) []RepoResult {
	out := make([]RepoResult, 0, len(o.Repos))
	for _, repo := range o.Repos {
		res := RepoResult{Repo: repo}
		_, err := o.restic(ctx, repo, "cat", "config")
		if exitCode(err) == 10 { // the repository does not exist
			_, err = o.restic(ctx, repo, "init")
		}
		res.Err = err
		out = append(out, res)
	}
	return out
}

// Opens reports, per repository, whether the password opens it; one that
// does not exist yet counts as opening (it would be created with it).
func Opens(ctx context.Context, o Options) []RepoResult {
	out := make([]RepoResult, 0, len(o.Repos))
	for _, repo := range o.Repos {
		res := RepoResult{Repo: repo}
		if _, err := o.restic(ctx, repo, "cat", "config"); err != nil && exitCode(err) != 10 {
			res.Err = err
		}
		out = append(out, res)
	}
	return out
}

func Run(ctx context.Context, a Archive, o Options) Report {
	var r Report
	need := make([]bool, len(o.Repos))
	unreachable := make([]error, len(o.Repos))
	anyNeed := false
	for i, repo := range o.Repos {
		need[i], unreachable[i] = o.needs(ctx, a, repo)
		anyNeed = anyNeed || need[i]
	}
	host, err := o.host(ctx, a)
	if err != nil {
		r.Integrity = err
		return r
	}
	if anyNeed {
		if err := a.IntegrityCheck(ctx); err != nil {
			r.Integrity = err
			return r
		}
		// The copy exists only for the snapshots: kept, it would take as much
		// room again as the archive's database.
		defer o.removeCopy()
		if err := o.prepare(ctx, a); err != nil {
			r.Integrity = fmt.Errorf("prepare the snapshot: %w", err)
			return r
		}
	}
	for i, repo := range o.Repos {
		if need[i] {
			r.Repos = append(r.Repos, o.backupRepo(ctx, a, repo, host))
			continue
		}
		if unreachable[i] != nil {
			r.Repos = append(r.Repos, RepoResult{Repo: repo, Err: unreachable[i]})
			continue
		}
		res := RepoResult{Repo: repo, Skipped: true}
		res.Snapshot, _, _, _ = a.LastSnapshot(ctx, repo)
		now := o.Now()
		// The place was just seen holding the archive as it is: it is up to date.
		if err := RecordSuccess(ctx, a, repo, now); err != nil {
			res.Err = err
			r.Repos = append(r.Repos, res)
			continue
		}
		if due(ctx, a, metaKey(repo, "prune_at"), o.PruneEvery, now) || due(ctx, a, metaKey(repo, "check_at"), o.CheckEvery, now) {
			_, _ = o.restic(ctx, repo, "unlock") // as before a snapshot
		}
		o.maintain(ctx, a, &res, now)
		r.Repos = append(r.Repos, res)
	}
	return r
}

// needs says whether repo needs a snapshot: its last full snapshot does not
// hold the latest update or the config as it is now, or it is no longer the
// repository that snapshot went to (wiped, made again). err: the place, which
// would need none, cannot be reached.
func (o Options) needs(ctx context.Context, a Archive, repo string) (bool, error) {
	if o.Force || o.Update == 0 {
		return true, nil
	}
	if _, n, ok, err := a.LastSnapshot(ctx, repo); err != nil || !ok || n != o.Update {
		return true, nil
	}
	if cfg, _, _ := a.Meta(ctx, metaKey(repo, "config")); cfg != o.configHash() {
		return true, nil
	}
	id, err := o.repoID(ctx, repo)
	if err != nil {
		return false, err
	}
	known, ok, _ := a.Meta(ctx, metaKey(repo, "repo_id"))
	if !ok {
		return false, a.SetMeta(ctx, metaKey(repo, "repo_id"), id)
	}
	return known != id, nil
}

// repoID is the repository's own id, which changes when it is made again.
func (o Options) repoID(ctx context.Context, repo string) (string, error) {
	out, err := o.restic(ctx, repo, "cat", "config")
	if err != nil {
		return "", err
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &c); err != nil || c.ID == "" {
		return "", fmt.Errorf("read the repository config of %s: %w", repo, cmp.Or(err, errors.New("no id")))
	}
	return c.ID, nil
}

func (o Options) configHash() string {
	b, err := os.ReadFile(o.ConfigPath)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// host is the name snapshots are tagged with. It is fixed at the first backup:
// a Mac's network name can change, and retention and the parent snapshot
// lookup only match snapshots of the same host.
func (o Options) host(ctx context.Context, a Archive) (string, error) {
	v, ok, err := a.Meta(ctx, "backup:host")
	if err != nil || ok {
		return v, err
	}
	return o.Host, a.SetMeta(ctx, "backup:host", o.Host)
}

// removeCopy deletes what prepare put into WorkDir.
func (o Options) removeCopy() {
	for _, f := range []string{"archive.sqlite", "config.yaml"} {
		_ = os.Remove(filepath.Join(o.WorkDir, f))
	}
	_ = os.Remove(o.WorkDir) // only if empty
}

// prepare puts a consistent database copy and the config into WorkDir.
func (o Options) prepare(ctx context.Context, a Archive) error {
	if err := a.VacuumInto(ctx, filepath.Join(o.WorkDir, "archive.sqlite")); err != nil {
		return err
	}
	cfg, err := os.ReadFile(o.ConfigPath)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o.WorkDir, "config.yaml"), cfg, 0o600)
}

func (o Options) backupRepo(ctx context.Context, a Archive, repo, host string) RepoResult {
	res := RepoResult{Repo: repo}
	now := o.Now()
	paths := []string{o.WorkDir}
	// html/ can be redrawn, but with it a snapshot restored by plain restic,
	// without tgbup, is readable in a browser.
	for _, d := range []string{"media", "html"} {
		if st, err := os.Stat(filepath.Join(o.ArchiveDir, d)); err == nil && st.IsDir() {
			paths = append(paths, filepath.Join(o.ArchiveDir, d))
		}
	}
	// A run killed earlier may have left its lock; unlock removes only locks of
	// processes that are gone. If the repository is unreachable, backup says so.
	_, _ = o.restic(ctx, repo, "unlock")
	// .<name>.part files are downloads in progress (media.PartPath); the
	// finished file comes in a later snapshot. A real file named x.part stays in.
	out, err := o.restic(ctx, repo, append([]string{"backup", "--json", "--tag", "tgbup", "--host", host, "--exclude", ".*.part"}, paths...)...)
	res.Snapshot = snapshotID(out)
	switch {
	case err != nil && exitCode(err) == 3 && res.Snapshot != "":
		// Some files could not be read, but the snapshot exists: keep going so retention still runs.
		res.Warning = err.(*resticError).msg
	case err != nil:
		res.Err = err
		return res
	}
	if err := RecordSuccess(ctx, a, repo, now); err != nil {
		res.Err = err
		return res
	}
	if res.Snapshot != "" {
		// A snapshot that missed files does not hold the update: the next run
		// makes another.
		if err := a.AddSnapshot(ctx, repo, res.Snapshot, o.Update, res.Warning != "", now); err != nil {
			res.Err = err
			return res
		}
		if err := a.SetMeta(ctx, metaKey(repo, "config"), o.configHash()); err != nil {
			res.Err = err
			return res
		}
		if id, err := o.repoID(ctx, repo); err == nil {
			_ = a.SetMeta(ctx, metaKey(repo, "repo_id"), id)
		}
	}
	k := o.Retention
	// Grouped by host and tag, not restic's default host and paths: the paths
	// change (html/ added, a folder missing on a run), and a group no new
	// snapshot joins would never be thinned.
	out, err = o.restic(ctx, repo, "forget", "--json", "--tag", "tgbup", "--host", host, "--group-by", "host,tags",
		"--keep-last", k.Last.Restic(), "--keep-daily", k.Daily.Restic(),
		"--keep-weekly", k.Weekly.Restic(), "--keep-monthly", k.Monthly.Restic())
	if err != nil {
		res.Err = err
		return res
	}
	if err := a.ForgetSnapshots(ctx, repo, forgotten(out), now); err != nil {
		res.Err = err
		return res
	}
	o.maintain(ctx, a, &res, now)
	return res
}

// forgotten lists the snapshots restic forget --json removed.
func forgotten(out []byte) []string {
	var groups []struct {
		Remove []struct {
			ID string `json:"id"`
		} `json:"remove"`
	}
	_ = json.Unmarshal(out, &groups)
	var ids []string
	for _, g := range groups {
		for _, s := range g.Remove {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// maintain prunes and checks repo when their intervals are due, also on a
// run that made no snapshot.
func (o Options) maintain(ctx context.Context, a Archive, res *RepoResult, now time.Time) {
	repo := res.Repo
	// prune rewrites pack files and check reads data back: both are costly on
	// cloud storage, so they run on their own intervals.
	if due(ctx, a, metaKey(repo, "prune_at"), o.PruneEvery, now) {
		if _, err := o.restic(ctx, repo, "prune"); err != nil {
			res.Err = err
			return
		}
		res.Pruned = true
		if err := a.SetMeta(ctx, metaKey(repo, "prune_at"), strconv.FormatInt(now.Unix(), 10)); err != nil {
			res.Err = err
			return
		}
	}
	if due(ctx, a, metaKey(repo, "check_at"), o.CheckEvery, now) {
		args := []string{"check"}
		if o.CheckSubset != "" {
			args = append(args, "--read-data-subset="+o.CheckSubset)
		}
		if _, err := o.restic(ctx, repo, args...); err != nil {
			res.Err = err
			return
		}
		res.Checked = true
		if err := a.SetMeta(ctx, metaKey(repo, "check_at"), strconv.FormatInt(now.Unix(), 10)); err != nil {
			res.Err = err
		}
	}
}

func due(ctx context.Context, a Archive, key string, every time.Duration, now time.Time) bool {
	last, ok := metaTime(ctx, a, key)
	return !ok || now.Sub(last) >= every
}

func snapshotID(jsonLines []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(jsonLines))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"message_type"`
			Snapshot string `json:"snapshot_id"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Type == "summary" {
			return m.Snapshot
		}
	}
	return ""
}

// restic runs one restic command against repo; the error carries what restic
// reported, which names the problem.
func (o Options) restic(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, o.Restic, append([]string{"-r", repo}, args...)...)
	cmd.Env = o.env()
	// On cancel (SIGTERM from launchd, Ctrl+C) ask restic to stop so it removes
	// its repository lock; a killed restic would leave the lock behind.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Minute
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, &resticError{op: args[0], repo: repo, msg: resticMessages(stderr.Bytes()), err: err}
	}
	return out, nil
}

// env is the environment restic runs in: the user's own RESTIC_* settings are
// dropped (RESTIC_PASSWORD_FILE would win over tgbup's password), and rclone's
// folder goes first on PATH, since restic splits -o rclone.program on spaces.
func (o Options) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "RESTIC_") || (o.Rclone != "" && strings.HasPrefix(kv, "PATH=")) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "RESTIC_PASSWORD="+o.Password)
	if o.RcloneConfigPass != "" {
		env = append(env, "RCLONE_CONFIG_PASS="+o.RcloneConfigPass)
	}
	if o.Rclone != "" {
		env = append(env, "PATH="+filepath.Dir(o.Rclone)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return env
}

type resticError struct {
	op, repo, msg string
	err           error
}

func (e *resticError) Error() string {
	return fmt.Sprintf("restic %s %s: %s", e.op, Display(e.repo), e.msg)
}

var addressPassword = regexp.MustCompile(`(://[^/@:\s]*):[^/@\s]*@`)

// Display is a place as pages, logs and messages show it: a password inside
// an address (rest:https://user:pass@host/) becomes ***, as restic shows it.
func Display(repo string) string { return addressPassword.ReplaceAllString(repo, "$1:***@") }
func (e *resticError) Unwrap() error { return e.err }

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// resticMessages turns restic's stderr (JSON lines with --json, plain text
// otherwise) into its last few messages.
func resticMessages(b []byte) string {
	var msgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		switch {
		case json.Unmarshal([]byte(line), &m) != nil:
			msgs = append(msgs, line)
		case m.Error.Message != "":
			msgs = append(msgs, m.Error.Message)
		case m.Message != "":
			msgs = append(msgs, m.Message)
		}
	}
	return lastLines([]byte(strings.Join(msgs, "\n")), 3)
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// Label names a place as people know it: the cloud folders macOS keeps under
// ~/Library by their app ("iCloud Drive › TelegramBackup"), a disk by its
// name, Google Drive through rclone; anything else by its path, ~ for home.
// A password inside an address is hidden (Display).
func Label(repo, home string) string {
	repo = Display(repo)
	join := func(head, rest string) string {
		parts := []string{head}
		for _, p := range strings.Split(rest, string(filepath.Separator)) {
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, " › ")
	}
	lib := filepath.Join(home, "Library")
	if rest, ok := strings.CutPrefix(repo, filepath.Join(lib, "Mobile Documents", "com~apple~CloudDocs")+string(filepath.Separator)); ok && home != "" {
		return join("iCloud Drive", rest)
	}
	if rest, ok := strings.CutPrefix(repo, filepath.Join(lib, "CloudStorage")+string(filepath.Separator)); ok && home != "" {
		app, rest, _ := strings.Cut(rest, string(filepath.Separator))
		name, acct, _ := strings.Cut(app, "-")
		switch name {
		case "GoogleDrive":
			name = "Google Drive"
		case "OneDrive", "Dropbox", "Box":
		default:
			name, acct = app, ""
		}
		if acct != "" {
			name += " (" + acct + ")"
		}
		return join(name, rest)
	}
	if rest, ok := strings.CutPrefix(repo, "/Volumes/"); ok {
		disk, rest, _ := strings.Cut(rest, "/")
		return join("disk «"+disk+"»", rest)
	}
	if rest, ok := strings.CutPrefix(repo, "rclone:gdrive:"); ok {
		return join("Google Drive (through rclone)", rest)
	}
	if home != "" && strings.HasPrefix(repo, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(repo, home)
	}
	return repo
}
