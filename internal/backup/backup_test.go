package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// resticPath finds a real restic: TGBUP_TEST_RESTIC, or one on PATH.
func resticPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("TGBUP_TEST_RESTIC"); p != "" {
		return p
	}
	p, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not available: set TGBUP_TEST_RESTIC or install restic")
	}
	return p
}

type env struct {
	o   Options
	db  *store.Store
	now time.Time
}

func newEnv(t *testing.T, repos ...string) *env {
	t.Helper()
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive")
	if err := os.MkdirAll(filepath.Join(archive, "media", "photo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "media", "photo", "7.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("archive_dir: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(archive, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{db: db, now: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	e.o = Options{
		Restic: resticPath(t), Repos: repos, Password: "test-password",
		Retention:  config.Retention{Last: 4, Daily: 7, Weekly: 4, Monthly: 12},
		PruneEvery: 24 * time.Hour, CheckEvery: 30 * 24 * time.Hour, CheckSubset: "5%",
		WorkDir: filepath.Join(dir, "work"), ArchiveDir: archive, ConfigPath: cfg, Host: "test-host",
		Now: func() time.Time { return e.now },
	}
	return e
}

func (e *env) restic(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command(e.o.Restic, append([]string{"-r", repo, "--no-cache"}, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+e.o.Password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restic %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestBackupSnapshotsArchive(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	if res := Init(ctx, e.o); len(res) != 1 || res[0].Err != nil {
		t.Fatalf("init = %+v", res)
	}
	// Init is idempotent.
	if res := Init(ctx, e.o); res[0].Err != nil {
		t.Fatalf("second init = %+v", res)
	}
	r := Run(ctx, e.db, e.o)
	if !r.OK() || r.Repos[0].Snapshot == "" {
		t.Fatalf("report = %+v", r)
	}
	files := e.restic(t, repo, "ls", "latest")
	for _, want := range []string{"/archive.sqlite", "/config.yaml", "/media/photo/7.jpg"} {
		if !strings.Contains(files, want) {
			t.Errorf("snapshot lacks %s:\n%s", want, files)
		}
	}
	if snaps := e.restic(t, repo, "snapshots", "--tag", "tgbup", "--host", "test-host"); !strings.Contains(snaps, r.Repos[0].Snapshot[:8]) {
		t.Errorf("snapshot not tagged for this host:\n%s", snaps)
	}
}

func TestPruneAndCheckFollowTheirIntervals(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	Init(ctx, e.o)
	step := func(name string, pruned, checked bool) {
		t.Helper()
		r := Run(ctx, e.db, e.o)
		if !r.OK() || r.Repos[0].Pruned != pruned || r.Repos[0].Checked != checked {
			t.Fatalf("%s: %+v", name, r.Repos[0])
		}
	}
	step("first run", true, true)
	step("same time", false, false)
	e.now = e.now.Add(25 * time.Hour)
	step("a day later", true, false)
	e.now = e.now.Add(30 * 24 * time.Hour)
	step("a month later", true, true)
}

func TestBrokenRepoDoesNotStopTheOthers(t *testing.T) {
	good := filepath.Join(t.TempDir(), "good")
	missing := filepath.Join(t.TempDir(), "never-initialised")
	e := newEnv(t, missing, good)
	ctx := context.Background()
	e.restic(t, good, "init")
	r := Run(ctx, e.db, e.o)
	if r.OK() || r.Repos[0].Err == nil || r.Repos[1].Err != nil || r.Repos[1].Snapshot == "" {
		t.Fatalf("report = %+v", r)
	}
	if _, ok, _ := e.db.Meta(ctx, metaKey(missing, "ok_at")); ok {
		t.Error("a failed repository was recorded as backed up")
	}
	if at, ok := LastSuccess(ctx, e.db, good); !ok || !at.Equal(e.now) {
		t.Errorf("last success of the good repository = %v %v", at, ok)
	}
}

type brokenArchive struct{ *store.Store }

func (brokenArchive) IntegrityCheck(context.Context) error {
	return errors.New("database disk image is malformed")
}

func TestDamagedArchiveIsNotBackedUp(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	Init(ctx, e.o)
	r := Run(ctx, brokenArchive{e.db}, e.o)
	if r.OK() || r.Integrity == nil || len(r.Repos) != 0 {
		t.Fatalf("report = %+v", r)
	}
	if snaps := e.restic(t, repo, "snapshots", "--json"); strings.TrimSpace(snaps) != "[]" {
		t.Fatalf("a damaged archive was snapshotted: %s", snaps)
	}
}

// A restic killed mid-run (Mac shut down, launchd stopped the job) leaves a
// lock behind; the next run must not fail on it.
func TestStaleLockIsCleared(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	Init(ctx, e.o)
	cmd := exec.Command(e.o.Restic, "-r", repo, "--no-cache", "backup", "--stdin", "--stdin-filename", "x")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+e.o.Password)
	stdin, _ := cmd.StdinPipe() // kept open: restic holds its lock waiting for data
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if locks, _ := os.ReadDir(filepath.Join(repo, "locks")); len(locks) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restic took no lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	stdin.Close()
	if r := Run(ctx, e.db, e.o); !r.OK() {
		t.Fatalf("run after a killed restic: %+v", r.Repos)
	}
}

// One unreadable file makes restic exit 3 but still write a snapshot: that is
// a warning, and retention must keep working.
func TestUnreadableFileIsAWarning(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	Init(ctx, e.o)
	locked := filepath.Join(e.o.ArchiveDir, "media", "photo", "locked.jpg")
	if err := os.WriteFile(locked, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	r := Run(ctx, e.db, e.o)
	if !r.OK() || r.Repos[0].Snapshot == "" || !strings.Contains(r.Repos[0].Warning, "locked.jpg") {
		t.Fatalf("report = %+v", r.Repos[0])
	}
}

// The host a snapshot is tagged with must stay the same when the Mac's network
// name changes, or retention and parent lookup stop matching old snapshots.
func TestHostIsKeptFromTheFirstBackup(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	Init(ctx, e.o)
	Run(ctx, e.db, e.o)
	e.o.Host = "renamed-by-dhcp"
	if r := Run(ctx, e.db, e.o); !r.OK() {
		t.Fatal(r)
	}
	if snaps := e.restic(t, repo, "snapshots", "--host", "renamed-by-dhcp", "--json"); strings.TrimSpace(snaps) != "[]" {
		t.Fatalf("snapshot taken under a new host: %s", snaps)
	}
}
