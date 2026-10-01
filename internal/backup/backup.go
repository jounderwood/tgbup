// Package backup snapshots the archive into restic repositories: a checked,
// consistent copy of the database, the config and media/, then forget by the
// retention policy, with prune and check on their own, longer intervals.
package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

// Archive is what a backup needs from the archive database.
type Archive interface {
	IntegrityCheck(ctx context.Context) error
	VacuumInto(ctx context.Context, path string) error
	Meta(ctx context.Context, key string) (string, bool, error)
	SetMeta(ctx context.Context, key, value string) error
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
}

type RepoResult struct {
	Repo     string
	Snapshot string
	Pruned   bool
	Checked  bool
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

func Run(ctx context.Context, a Archive, o Options) Report {
	var r Report
	if err := a.IntegrityCheck(ctx); err != nil {
		r.Integrity = err
		return r
	}
	if err := o.prepare(ctx, a); err != nil {
		r.Integrity = fmt.Errorf("prepare the snapshot: %w", err)
		return r
	}
	host, err := o.host(ctx, a)
	if err != nil {
		r.Integrity = err
		return r
	}
	for _, repo := range o.Repos {
		r.Repos = append(r.Repos, o.backupRepo(ctx, a, repo, host))
	}
	return r
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
	if st, err := os.Stat(filepath.Join(o.ArchiveDir, "media")); err == nil && st.IsDir() {
		paths = append(paths, filepath.Join(o.ArchiveDir, "media"))
	}
	// A run killed earlier may have left its lock; unlock removes only locks of
	// processes that are gone. If the repository is unreachable, backup says so.
	_, _ = o.restic(ctx, repo, "unlock")
	out, err := o.restic(ctx, repo, append([]string{"backup", "--json", "--tag", "tgbup", "--host", host}, paths...)...)
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
	k := o.Retention
	if _, err := o.restic(ctx, repo, "forget", "--tag", "tgbup", "--host", host,
		"--keep-last", strconv.Itoa(k.Last), "--keep-daily", strconv.Itoa(k.Daily),
		"--keep-weekly", strconv.Itoa(k.Weekly), "--keep-monthly", strconv.Itoa(k.Monthly)); err != nil {
		res.Err = err
		return res
	}
	// prune rewrites pack files and check reads data back: both are costly on
	// cloud storage, so they run on their own intervals.
	if due(ctx, a, metaKey(repo, "prune_at"), o.PruneEvery, now) {
		if _, err := o.restic(ctx, repo, "prune"); err != nil {
			res.Err = err
			return res
		}
		res.Pruned = true
		if err := a.SetMeta(ctx, metaKey(repo, "prune_at"), strconv.FormatInt(now.Unix(), 10)); err != nil {
			res.Err = err
			return res
		}
	}
	if due(ctx, a, metaKey(repo, "check_at"), o.CheckEvery, now) {
		args := []string{"check"}
		if o.CheckSubset != "" {
			args = append(args, "--read-data-subset="+o.CheckSubset)
		}
		if _, err := o.restic(ctx, repo, args...); err != nil {
			res.Err = err
			return res
		}
		res.Checked = true
		if err := a.SetMeta(ctx, metaKey(repo, "check_at"), strconv.FormatInt(now.Unix(), 10)); err != nil {
			res.Err = err
		}
	}
	return res
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

func (e *resticError) Error() string { return fmt.Sprintf("restic %s %s: %s", e.op, e.repo, e.msg) }
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
