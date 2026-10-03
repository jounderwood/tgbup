package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/backup"
	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// testWizard runs against files in a temp home, reading input from a string.
func testWizard(t *testing.T, input string) (*wizard, *bytes.Buffer, config.Paths) {
	t.Helper()
	home := t.TempDir()
	vars := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin", "SHELL": "/bin/zsh"}
	var out bytes.Buffer
	env := Env{Stdin: strings.NewReader(input), Stdout: &out, Stderr: &out, Getenv: func(k string) string { return vars[k] }}
	p, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return &wizard{env: env, out: &out, in: newLineReader(env.Stdin, nil)}, &out, p
}

func TestRunStepsSkipsDoneStepsAndStopsAtFailure(t *testing.T) {
	w, out, _ := testWizard(t, "")
	var ran []string
	step := func(name string, done bool, err error) initStep {
		return initStep{name: name,
			check: func(context.Context) (string, bool, error) { return name + " ok", done, nil },
			run:   func(context.Context) error { ran = append(ran, name); return err }}
	}
	steps := []initStep{
		step("keys", true, nil),
		step("bot", false, errors.New("network down")),
		step("chats", false, errSkipped),
		step("backup", false, errors.New("disk full")),
		step("schedule", false, nil),
	}
	steps[1].optional = true
	skipped, ok := runSteps(context.Background(), w, steps)
	if ok {
		t.Fatal("reported success after a required step failed")
	}
	if !slices.Equal(skipped, []string{"bot", "chats"}) {
		t.Errorf("skipped = %v", skipped)
	}
	if !slices.Equal(ran, []string{"bot", "chats", "backup"}) {
		t.Errorf("ran %v: a done step must not run, nothing after a failure", ran)
	}
	for _, want := range []string{"[1/5] keys", "✓ keys ok", "✗ network down", "optional", "skipped", "✗ disk full", "run tgbup init again"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunStepsOffersToRedoAndStopsAtEOF(t *testing.T) {
	w, out, _ := testWizard(t, "y\n")
	var ran []string
	steps := []initStep{
		{name: "chats", change: "Change?", check: func(context.Context) (string, bool, error) { return "3 chats", true, nil },
			run: func(context.Context) error { ran = append(ran, "chats"); return nil }},
		{name: "storage", change: "Change?", check: func(context.Context) (string, bool, error) { return "icloud", true, nil },
			run: func(context.Context) error { ran = append(ran, "storage"); return nil }},
	}
	if _, ok := runSteps(context.Background(), w, steps); ok {
		t.Fatal("success although input ended at a question")
	}
	if !slices.Equal(ran, []string{"chats"}) {
		t.Errorf("ran %v", ran)
	}
	if !strings.Contains(out.String(), "Stopped. Run tgbup init again") {
		t.Errorf("no resume hint:\n%s", out.String())
	}
}

func TestSetKeysValidatesAndSaves(t *testing.T) {
	w, out, p := testWizard(t, "abc\n12345\nnot-a-hash\n0123456789ABCDEF0123456789abcdef\n")
	if err := w.setKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec, err := config.LoadSecrets(p.Secrets, func(string) string { return "" })
	if err != nil || sec.APIID != 12345 || sec.APIHash != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("secrets = %+v, %v", sec, err)
	}
	for _, want := range []string{"api_id is a number", "api_hash is 32 characters"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if _, done, _ := w.checkKeys(context.Background()); !done {
		t.Error("keys not seen as done")
	}
}

func TestSetPasswordGeneratesAndWaitsForSaved(t *testing.T) {
	w, out, p := testWizard(t, "\nok\nsaved\n")
	if err := w.setPassword(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec, _ := config.LoadSecrets(p.Secrets, func(string) string { return "" })
	if !strings.Contains(out.String(), sec.ResticPassword) || len(sec.ResticPassword) != 35 {
		t.Fatalf("password %q not shown or wrong length:\n%s", sec.ResticPassword, out.String())
	}
	if strings.Count(out.String(), "then type \"saved\"") != 2 {
		t.Errorf("\"ok\" accepted as saved:\n%s", out.String())
	}
}

func TestSetPasswordStopsWithoutSavingWhenUnconfirmed(t *testing.T) {
	w, _, p := testWizard(t, "\n")
	if err := w.setPassword(context.Background()); !errors.Is(err, errStopped) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p.Secrets); err == nil {
		t.Fatal("a password nobody saved elsewhere was written")
	}
}

func TestSetPasswordOwn(t *testing.T) {
	w, out, p := testWizard(t, "n\nshort\nlong enough pass\nother one here\nlong enough pass\nlong enough pass\nsaved\n")
	if err := w.setPassword(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec, _ := config.LoadSecrets(p.Secrets, func(string) string { return "" })
	if sec.ResticPassword != "long enough pass" {
		t.Fatalf("password = %q", sec.ResticPassword)
	}
	for _, want := range []string{"too short", "do not match"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q", want)
		}
	}
}

func TestNewPassword(t *testing.T) {
	a, b := newPassword(), newPassword()
	if !regexp.MustCompile(`^([a-hjkmnp-z2-9]{5}-){5}[a-hjkmnp-z2-9]{5}$`).MatchString(a) || a == b {
		t.Fatalf("passwords %q %q", a, b)
	}
}

func TestStorageChoicesFindCloudFolders(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Library/Mobile Documents/com~apple~CloudDocs", "Library/CloudStorage/GoogleDrive-me@x.com/My Drive", "Library/CloudStorage/Dropbox"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := storageChoices(home)
	var repos, defaults []string
	for _, c := range got {
		repos = append(repos, tildePath(c.Repo, home))
		if c.Default {
			defaults = append(defaults, c.Repo)
		}
	}
	want := []string{
		"~/Library/Mobile Documents/com~apple~CloudDocs/tgbup-restic", "rclone:gdrive:tgbup-restic",
		"~/Library/CloudStorage/Dropbox/tgbup-restic", "~/Library/CloudStorage/GoogleDrive-me@x.com/My Drive/tgbup-restic", "",
	}
	if !slices.Equal(repos, want) {
		t.Errorf("repos = %q", repos)
	}
	if len(defaults) != 2 {
		t.Errorf("defaults = %q, want iCloud and Google Drive", defaults)
	}
	if !strings.Contains(got[3].Label, "me@x.com") {
		t.Errorf("label = %q", got[3].Label)
	}

	// No iCloud: only Google Drive is preselected.
	if c := storageChoices(t.TempDir()); len(c) != 2 || !c[0].Default || c[1].Repo != "" {
		t.Errorf("choices without cloud folders = %+v", c)
	}
}

func TestChooseStorageWritesRepositories(t *testing.T) {
	w, out, p := testWizard(t, "9\n1,2\n/Volumes/NAS/restic\n")
	if err := w.chooseStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithoutTargets(p.Config, p.Home)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Backup.Repositories, []string{"rclone:gdrive:tgbup-restic", "/Volumes/NAS/restic"}) {
		t.Errorf("repositories = %q", cfg.Backup.Repositories)
	}
	if !strings.Contains(out.String(), `no choice "9"`) {
		t.Errorf("bad choice not reported:\n%s", out.String())
	}
}

func TestChooseStorageDefaultsOnEnter(t *testing.T) {
	w, _, p := testWizard(t, "\n")
	if err := os.MkdirAll(filepath.Join(p.Home, "Library/Mobile Documents/com~apple~CloudDocs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.chooseStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p.Config)
	if !strings.Contains(string(raw), "~/Library/Mobile Documents/com~apple~CloudDocs/tgbup-restic") || !strings.Contains(string(raw), "rclone:gdrive:tgbup-restic") {
		t.Errorf("config:\n%s", raw)
	}
}

func TestParseChoice(t *testing.T) {
	got, err := parseChoice("3, 1-2,3", 4)
	if err != nil || !slices.Equal(got, []int{2, 0, 1}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"0", "5", "2-1", "x", ""} {
		if _, err := parseChoice(bad, 4); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRcloneRemote(t *testing.T) {
	for repo, want := range map[string]string{"rclone:gdrive:tgbup-restic": "gdrive", "rclone:b2:x/y": "b2", "/local": "", "rclone:": ""} {
		if got, _ := rcloneRemote(repo); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
}

func TestSetArchiveDir(t *testing.T) {
	w, _, _ := testWizard(t, "relative/dir\n")
	if err := w.setArchiveDir(context.Background()); err == nil {
		t.Fatal("relative path accepted")
	}
	w, _, _ = testWizard(t, "\n")
	if _, done, _ := w.checkArchiveDir(context.Background()); done {
		t.Fatal("done before the config exists")
	}
	if err := w.setArchiveDir(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, done, err := w.checkArchiveDir(context.Background())
	if err != nil || !done || status != "~/TelegramArchive" {
		t.Fatalf("check = %q %v %v", status, done, err)
	}
}

func TestAddPathOnce(t *testing.T) {
	w, _, p := testWizard(t, "\n\n")
	profile := filepath.Join(p.Home, ".zprofile")
	if err := os.WriteFile(profile, []byte("alias ll='ls -l'"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := w.addPath(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(profile)
	if strings.Count(string(raw), "export PATH=") != 1 || !strings.HasPrefix(string(raw), "alias ll='ls -l'\n# tgbup\n") {
		t.Errorf(".zprofile:\n%s", raw)
	}
}

func TestProfileLine(t *testing.T) {
	if got := profileLine("/Users/me/.local/bin", "/Users/me"); got != `export PATH="$HOME/.local/bin:$PATH"` {
		t.Errorf("got %s", got)
	}
	if got := profileLine("/opt/tgbup", "/Users/me"); got != `export PATH="/opt/tgbup:$PATH"` {
		t.Errorf("got %s", got)
	}
}

// launchdRun fakes a job that runs for a few polls and exits with code.
type launchdRun struct {
	polls, runFor int
	code          string
	started       bool
}

func (f *launchdRun) launchctl(args ...string) ([]byte, error) {
	switch args[0] {
	case "kickstart":
		f.started = true
	case "print":
		if f.started && f.polls < f.runFor {
			f.polls++
			return []byte("\tstate = running\n\tlast exit code = (never exited)\n\t\tstate = active\n"), nil
		}
		if f.started {
			return []byte("\tstate = not running\n\tlast exit code = " + f.code + "\n"), nil
		}
		return []byte("\tstate = not running\n\tlast exit code = (never exited)\n"), nil
	}
	return nil, nil
}

func TestWatchRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		record  *store.Run // the run the job records, if any
		code    string
		wantErr bool
		want    string
	}{
		{"ok", &store.Run{OK: true}, "0", false, "the scheduled run worked"},
		{"errors", &store.Run{OK: false, Summary: `{"backup_errors":["restic backup icloud: disk full"]}`}, "1", true, "disk full"},
		{"no record", nil, "1", true, "Full Disk Access"},
	} {
		w, out, _ := testWizard(t, "")
		archive := t.TempDir()
		f := &launchdRun{runFor: 3, code: tc.code}
		j, _ := testSchedule(t)
		j.launchctl = func(args ...string) ([]byte, error) {
			if args[0] == "kickstart" && tc.record != nil {
				db, err := store.Open(filepath.Join(archive, "archive.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				r := *tc.record
				r.Started, r.Finished = time.Now(), time.Now()
				if err := db.AddRun(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			return f.launchctl(args...)
		}
		if err := os.MkdirAll(filepath.Dir(j.LogPath), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(j.LogPath, []byte("level=ERROR msg=backup err=\"open iCloud: operation not permitted\"\n"), 0o600)
		err := w.watchRun(context.Background(), j, archive, time.Millisecond, time.Minute)
		if (err != nil) != tc.wantErr || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: err=%v output:\n%s", tc.name, err, out.String())
		}
	}
}

func TestWatchRunGivesUpAsSkipped(t *testing.T) {
	w, _, _ := testWizard(t, "")
	j, _ := testSchedule(t)
	j.launchctl = (&launchdRun{runFor: 1 << 30}).launchctl
	if err := w.watchRun(context.Background(), j, t.TempDir(), time.Millisecond, 20*time.Millisecond); !errors.Is(err, errSkipped) {
		t.Fatalf("err = %v: an unchecked run must not count as done", err)
	}
}

func TestTestServersLeaveScheduleAndPathAlone(t *testing.T) {
	w, _, _ := testWizard(t, "")
	getenv := w.env.Getenv
	w.env.Getenv = func(k string) string {
		if k == "TGBUP_TEST_DC" {
			return "1"
		}
		return getenv(k)
	}
	for name, check := range map[string]func(context.Context) (string, bool, error){"schedule": w.checkSchedule, "path": w.checkPath} {
		status, done, err := check(context.Background())
		if err != nil || !done || !strings.Contains(status, "test servers") {
			t.Errorf("%s: %q %v %v", name, status, done, err)
		}
	}
	var out bytes.Buffer
	if code := cmdSchedule([]string{"install"}, Env{Stdout: &out, Stderr: &out, Getenv: w.env.Getenv}); code == 0 || !strings.Contains(out.String(), "TGBUP_TEST_DC") {
		t.Errorf("schedule install on test servers: code %d, %s", code, out.String())
	}
}

func TestKeysCanBeReplacedWhenBroken(t *testing.T) {
	w, _, p := testWizard(t, "777\n0123456789abcdef0123456789abcdef\n")
	if err := os.MkdirAll(filepath.Dir(p.Secrets), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Secrets, []byte("TG_API_ID=12a\nTG_API_HASH=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, done, err := w.checkKeys(context.Background()); done || err != nil {
		t.Fatalf("broken api_id: done=%v err=%v, want a step to fix it", done, err)
	}
	if err := w.setKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, done, _ := w.checkKeys(context.Background()); !done || !strings.Contains(status, "777") {
		t.Fatalf("after fixing: %q %v", status, done)
	}
}

func TestOwnPasswordMustReadBackTheSame(t *testing.T) {
	w, out, _ := testWizard(t, "\" quoted password\"\n trailing space pw \ngood password 1\ngood password 1\n")
	got, err := w.ownPassword(context.Background())
	if err != nil || got != "good password 1" {
		t.Fatalf("got %q, %v", got, err)
	}
	if strings.Count(out.String(), "must not start or end") != 2 {
		t.Errorf("output:\n%s", out.String())
	}
}

// fakeRclone writes a shell script standing in for rclone: remotes live in a
// file, "lsd" fails while the remote is marked broken.
func fakeRclone(t *testing.T, remotes string, broken bool) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "remotes"), []byte(remotes), 0o600)
	if broken {
		_ = os.WriteFile(filepath.Join(dir, "broken"), nil, 0o600)
	}
	script := `#!/bin/sh
d=` + dir + `
echo "$@" >> $d/calls
case "$1" in
listremotes) cat $d/remotes ;;
lsd) if [ -e $d/broken ]; then echo "Failed to lsd: couldn't fetch token: invalid_grant" >&2; exit 1; fi ;;
config) if [ "$2" = create ]; then echo "$3:" >> $d/remotes; fi; rm -f $d/broken ;;
esac
`
	path = filepath.Join(dir, "rclone")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func TestEnsureRcloneRemote(t *testing.T) {
	for _, tc := range []struct {
		name, remotes, input string
		broken               bool
		wantCalls            string
		wantErr              bool
	}{
		{"works", "gdrive:\n", "", false, "listremotes|lsd gdrive:", false},
		{"missing, connect", "", "\n", false, "listremotes|config create gdrive drive scope=drive.file|listremotes|lsd gdrive:", false},
		{"missing, declined", "", "n\n", false, "listremotes", true},
		{"broken token, reconnect", "gdrive:\n", "\n", true, "listremotes|lsd gdrive:|config reconnect gdrive:|lsd gdrive:", false},
		{"other remote missing", "", "", false, "listremotes", true},
	} {
		w, out, _ := testWizard(t, tc.input)
		bin, dir := fakeRclone(t, tc.remotes, tc.broken)
		remote := "gdrive"
		if tc.name == "other remote missing" {
			remote = "b2"
		}
		err := w.ensureRcloneRemote(context.Background(), backup.Options{Rclone: bin}, remote)
		calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
		got := strings.ReplaceAll(strings.TrimSpace(string(calls)), "\n", "|")
		if (err != nil) != tc.wantErr || got != tc.wantCalls {
			t.Errorf("%s: err=%v calls=%q\n%s", tc.name, err, got, out.String())
		}
	}
}

func TestJobState(t *testing.T) {
	j, _ := testSchedule(t)
	j.launchctl = (&launchdRun{started: true, runFor: 1}).launchctl
	if s := j.state(); !s.Loaded || !s.Running || s.LastExit != "" {
		t.Errorf("running job: %+v (nested state = active must not count)", s)
	}
	j.launchctl = func(...string) ([]byte, error) { return nil, errors.New("not found") }
	if s := j.state(); s.Loaded {
		t.Errorf("missing job: %+v", s)
	}
}
