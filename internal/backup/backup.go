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
	Err      error
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
		var exit *exec.ExitError
		if errors.As(errors.Unwrap(err), &exit) && exit.ExitCode() == 10 { // 10: repository does not exist
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
	for _, repo := range o.Repos {
		r.Repos = append(r.Repos, o.backupRepo(ctx, a, repo))
	}
	return r
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

func (o Options) backupRepo(ctx context.Context, a Archive, repo string) RepoResult {
	res := RepoResult{Repo: repo}
	now := o.Now()
	paths := []string{o.WorkDir}
	if st, err := os.Stat(filepath.Join(o.ArchiveDir, "media")); err == nil && st.IsDir() {
		paths = append(paths, filepath.Join(o.ArchiveDir, "media"))
	}
	out, err := o.restic(ctx, repo, append([]string{"backup", "--json", "--tag", "tgbup", "--host", o.Host}, paths...)...)
	if err != nil {
		res.Err = err
		return res
	}
	res.Snapshot = snapshotID(out)
	if err := a.SetMeta(ctx, metaKey(repo, "ok_at"), strconv.FormatInt(now.Unix(), 10)); err != nil {
		res.Err = err
		return res
	}
	k := o.Retention
	if _, err := o.restic(ctx, repo, "forget", "--tag", "tgbup", "--host", o.Host,
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

// restic runs one restic command against repo; the error carries the last
// lines restic printed, which name the problem.
func (o Options) restic(ctx context.Context, repo string, args ...string) ([]byte, error) {
	full := []string{"-r", repo}
	if strings.HasPrefix(repo, "rclone:") && o.Rclone != "" {
		full = append(full, "-o", "rclone.program="+o.Rclone)
	}
	cmd := exec.CommandContext(ctx, o.Restic, append(full, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+o.Password)
	if o.RcloneConfigPass != "" {
		cmd.Env = append(cmd.Env, "RCLONE_CONFIG_PASS="+o.RcloneConfigPass)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("restic %s %s: %s: %w", args[0], repo, lastLines(stderr.Bytes(), 3), err)
	}
	return out, nil
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
