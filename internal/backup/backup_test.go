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

	"github.com/joeunderwood/tgbup/internal/config"
	"github.com/joeunderwood/tgbup/internal/store"
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
	for _, d := range []string{"html", "md"} {
		if err := os.MkdirAll(filepath.Join(archive, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archive, d, "index."+d), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
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
	// The database copy is only for the snapshot.
	if _, err := os.Stat(filepath.Join(e.o.WorkDir, "archive.sqlite")); err == nil {
		t.Error("the database copy stays after the backup")
	}
	files := e.restic(t, repo, "ls", "latest")
	for _, want := range []string{"/archive.sqlite", "/config.yaml", "/media/photo/7.jpg", "/html/index.html"} {
		if !strings.Contains(files, want) {
			t.Errorf("snapshot lacks %s:\n%s", want, files)
		}
	}
	if strings.Contains(files, "/md/") {
		t.Errorf("md/ is the same as html/ and stays out:\n%s", files)
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
	e.o.Update = 1
	r := Run(ctx, e.db, e.o)
	if !r.OK() || r.Repos[0].Snapshot == "" || !strings.Contains(r.Repos[0].Warning, "locked.jpg") {
		t.Fatalf("report = %+v", r.Repos[0])
	}
	// It misses a file, so it does not hold the update: the next run tries again.
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	if again := Run(ctx, e.db, e.o); again.Repos[0].Skipped || again.Repos[0].Warning != "" {
		t.Fatalf("after a partial snapshot = %+v", again.Repos[0])
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

func TestRestoreBringsTheArchiveBackWithoutDeletingAnything(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	ctx := context.Background()
	if res := Init(ctx, e.o); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if r := Run(ctx, e.db, e.o); !r.OK() {
		t.Fatalf("backup = %+v", r)
	}
	snaps, err := Snapshots(ctx, e.o, repo)
	if err != nil || len(snaps) != 1 || len(snaps[0].Paths) != 3 {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}

	// A new Mac: empty folder.
	fresh := filepath.Join(t.TempDir(), "TelegramArchive")
	res, err := Restore(ctx, e.o, repo, "latest", fresh, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if res.MediaAdded != 1 || res.MovedAside != "" || !strings.Contains(string(res.Config), "archive_dir") {
		t.Errorf("result = %+v", res)
	}
	if info, err := os.Stat(fresh); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("a restored archive must be the owner's only: %v %v", info.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "html")); !os.IsNotExist(err) {
		t.Errorf("html/ restored, but tgbup redraws it: %v", err)
	}
	db, err := store.Open(filepath.Join(fresh, "archive.sqlite"))
	if err != nil {
		t.Fatalf("restored database does not open: %v", err)
	}
	db.Close()
	if b, _ := os.ReadFile(filepath.Join(fresh, "media", "photo", "7.jpg")); string(b) != "jpeg" {
		t.Errorf("media = %q", b)
	}
	if entries, _ := os.ReadDir(fresh); len(entries) != 2 {
		t.Errorf("left behind: %v", entries)
	}

	// Restoring over the same archive: its database is kept aside, the same media stay.
	res, err = Restore(ctx, e.o, repo, snaps[0].ShortID, fresh, e.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.MovedAside == "" || !isFile(res.MovedAside) || res.MediaKept != 1 || res.MediaAdded != 0 || res.MediaReplaced != 0 {
		t.Errorf("second restore = %+v", res)
	}

	// A different file under a snapshot file's name (names are not ids) is
	// moved aside, not deleted, and the restored database finds its own file.
	if err := os.WriteFile(filepath.Join(fresh, "media", "photo", "7.jpg"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = Restore(ctx, e.o, repo, snaps[0].ShortID, fresh, e.now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.MediaReplaced != 1 || res.MediaAdded != 1 || res.MediaAside == "" {
		t.Errorf("third restore = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(fresh, "media", "photo", "7.jpg")); string(b) != "jpeg" {
		t.Errorf("restored file = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(res.MediaAside, "photo", "7.jpg")); string(b) != "other" {
		t.Errorf("the other file was not kept aside: %q", b)
	}
	if _, err := Restore(ctx, e.o, repo, "nope", fresh, e.now); err == nil {
		t.Error("unknown snapshot accepted")
	}
}

// Snapshots taken with other folders (html/ added, a folder missing on a run)
// are thinned together with the rest, not kept forever as their own group.
func TestRetentionIgnoresChangedFolders(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	e := newEnv(t, repo)
	e.o.Retention = config.Retention{Last: 1}
	ctx := context.Background()
	if res := Init(ctx, e.o); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if r := Run(ctx, e.db, e.o); !r.OK() {
		t.Fatalf("first backup = %+v", r)
	}
	if err := os.RemoveAll(filepath.Join(e.o.ArchiveDir, "html")); err != nil {
		t.Fatal(err)
	}
	if r := Run(ctx, e.db, e.o); !r.OK() {
		t.Fatalf("second backup = %+v", r)
	}
	if snaps, err := Snapshots(ctx, e.o, repo); err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots = %d %v, want 1", len(snaps), err)
	}
}

func TestOpensTellsAWrongPassword(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	missing := filepath.Join(t.TempDir(), "missing")
	e := newEnv(t, repo, missing)
	ctx := context.Background()
	if res := Init(ctx, Options{Restic: e.o.Restic, Repos: []string{repo}, Password: e.o.Password}); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if res := Opens(ctx, e.o); res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("right password: %+v", res)
	}
	e.o.Password = "wrong-password"
	if res := Opens(ctx, e.o); res[0].Err == nil || res[1].Err != nil {
		t.Fatalf("wrong password: %+v (a missing repository opens with any)", res)
	}
}

func TestUnchangedArchiveGetsNoNewSnapshot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	second := filepath.Join(t.TempDir(), "second")
	e := newEnv(t, repo)
	e.o.Retention = config.Retention{Last: 1}
	ctx := context.Background()
	if res := Init(ctx, Options{Restic: e.o.Restic, Password: e.o.Password, Repos: []string{repo, second}}); res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("init = %+v", res)
	}
	e.o.Update = 1
	first := Run(ctx, e.db, e.o)
	if !first.OK() || first.Repos[0].Skipped || first.Repos[0].Snapshot == "" {
		t.Fatalf("first = %+v", first)
	}
	if id, n, ok, _ := e.db.LastSnapshot(ctx, repo); !ok || id != first.Repos[0].Snapshot || n != 1 {
		t.Fatalf("recorded = %q %d %v", id, n, ok)
	}

	// The same update again: nothing to snapshot, not even a database copy.
	e.now = e.now.Add(time.Hour)
	again := Run(ctx, e.db, e.o)
	if !again.OK() || !again.Repos[0].Skipped || again.Repos[0].Snapshot != first.Repos[0].Snapshot {
		t.Fatalf("again = %+v", again)
	}
	if snaps, _ := Snapshots(ctx, e.o, repo); len(snaps) != 1 {
		t.Fatalf("snapshots after an unchanged run = %d, want 1", len(snaps))
	}

	// A new place has no snapshot of this update yet: it gets one, the old place not.
	e.o.Repos = []string{repo, second}
	added := Run(ctx, e.db, e.o)
	if !added.OK() || !added.Repos[0].Skipped || added.Repos[1].Skipped || added.Repos[1].Snapshot == "" {
		t.Fatalf("added place = %+v", added)
	}

	// A new update: a new snapshot; retention (keep 1) removes the first, and
	// the archive knows it.
	e.o.Repos, e.o.Update = []string{repo}, 2
	e.now = e.now.Add(time.Hour)
	next := Run(ctx, e.db, e.o)
	if !next.OK() || next.Repos[0].Skipped || next.Repos[0].Snapshot == first.Repos[0].Snapshot {
		t.Fatalf("next = %+v", next)
	}
	var gone bool
	for _, id := range forgottenIDs(t, e, repo) {
		gone = gone || id == first.Repos[0].Snapshot
	}
	if !gone {
		t.Fatal("the archive does not know retention removed the first snapshot")
	}
	if id, n, _, _ := e.db.LastSnapshot(ctx, repo); id != next.Repos[0].Snapshot || n != 2 {
		t.Fatalf("last = %q %d", id, n)
	}

	// A changed config goes into a snapshot even without new messages.
	e.now = e.now.Add(time.Hour)
	if err := os.WriteFile(e.o.ConfigPath, []byte("archive_dir: y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Run(ctx, e.db, e.o); r.Repos[0].Skipped {
		t.Fatalf("config changed, skipped: %+v", r.Repos[0])
	}
	if r := Run(ctx, e.db, e.o); !r.Repos[0].Skipped {
		t.Fatalf("config unchanged since, not skipped: %+v", r.Repos[0])
	}

	// A place made again from scratch at the same path gets a snapshot.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if r := Run(ctx, e.db, e.o); r.Repos[0].Err == nil {
		t.Fatalf("a missing place is not an error: %+v", r.Repos[0])
	}
	Init(ctx, e.o)
	if r := Run(ctx, e.db, e.o); !r.OK() || r.Repos[0].Skipped {
		t.Fatalf("a new repository at the same path: %+v", r.Repos[0])
	}

	// Force (tgbup backup by hand) snapshots even an unchanged archive.
	e.o.Force = true
	if forced := Run(ctx, e.db, e.o); !forced.OK() || forced.Repos[0].Skipped || forced.Repos[0].Snapshot == next.Repos[0].Snapshot {
		t.Fatalf("forced = %+v", forced)
	}
}

func TestForgottenReadsForgetJSON(t *testing.T) {
	out := []byte(`[{"tags":["tgbup"],"host":"h","paths":["/a"],"keep":[{"id":"k1"}],"remove":[{"id":"r1"},{"id":"r2"}],"reasons":[]},` +
		`{"tags":["tgbup"],"host":"h","paths":["/b"],"keep":[{"id":"k2"}],"remove":null,"reasons":[]}]`)
	if got := forgotten(out); strings.Join(got, ",") != "r1,r2" {
		t.Fatalf("forgotten = %v", got)
	}
	if got := forgotten([]byte("not json")); len(got) != 0 {
		t.Fatalf("bad output = %v", got)
	}
}

// forgottenIDs lists the snapshots of repo the archive marked as removed.
func forgottenIDs(t *testing.T, e *env, repo string) []string {
	t.Helper()
	ids, err := e.db.ForgottenSnapshots(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}
