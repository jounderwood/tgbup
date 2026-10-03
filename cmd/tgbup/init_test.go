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
	w := &wizard{env: env, out: &out, in: newLineReader(env.Stdin, nil)}
	w.initRepos = func(context.Context) error { return nil }
	return w, &out, p
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

func TestChoosePasswordGeneratesAndWaitsForSaved(t *testing.T) {
	w, out, _ := testWizard(t, "\nok\nsaved\n")
	pass, err := w.choosePassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), pass) || len(pass) != 35 {
		t.Fatalf("password %q not shown or wrong length:\n%s", pass, out.String())
	}
	if strings.Count(out.String(), "then type \"saved\"") != 2 {
		t.Errorf("\"ok\" accepted as saved:\n%s", out.String())
	}
}

func TestChoosePasswordStopsWhenUnconfirmed(t *testing.T) {
	w, _, _ := testWizard(t, "\n")
	if _, err := w.choosePassword(context.Background()); !errors.Is(err, errStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestChoosePasswordOwn(t *testing.T) {
	w, out, _ := testWizard(t, "n\nshort\nlong enough pass\nother one here\nlong enough pass\nlong enough pass\nsaved\n")
	pass, err := w.choosePassword(context.Background())
	if err != nil || pass != "long enough pass" {
		t.Fatalf("password = %q, %v", pass, err)
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
	w, out, _ := testWizard(t, "\nrelative/dir\n")
	if err := w.setArchiveDir(context.Background()); err == nil || !strings.Contains(out.String(), "full path") {
		t.Fatalf("relative path accepted: %v", err)
	}
	w, _, _ = testWizard(t, "\n\n")
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
		switch k {
		case "TGBUP_TEST_DC":
			return "1"
		case "TGBUP_CONFIG":
			return filepath.Join(getenv("HOME"), "test", "config.yaml")
		case "TGBUP_SECRETS":
			return filepath.Join(getenv("HOME"), "test", "secrets.env")
		case "TGBUP_STATE_DIR":
			return filepath.Join(getenv("HOME"), "test", "state")
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

func TestDeclinedIsNotMissingAndAfterRunsForDoneSteps(t *testing.T) {
	w, out, _ := testWizard(t, "\n")
	checked := false
	steps := []initStep{
		{name: "backup", optional: true, run: func(context.Context) error { return errDeclined }},
		{name: "storage", change: "Change?", check: func(context.Context) (string, bool, error) { return "icloud", true, nil },
			run:   func(context.Context) error { t.Error("redone without a yes"); return nil },
			after: func(context.Context) error { checked = true; return nil }},
	}
	skipped, ok := runSteps(context.Background(), w, steps)
	if !ok || len(skipped) != 0 || !checked {
		t.Fatalf("ok=%v skipped=%v checked=%v\n%s", ok, skipped, checked, out.String())
	}
	if !strings.Contains(out.String(), "– off") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestStorageChoicesFindCloudFoldersAndDisks(t *testing.T) {
	home, vols := t.TempDir(), t.TempDir()
	for _, d := range []string{"Library/Mobile Documents/com~apple~CloudDocs", "Library/CloudStorage/GoogleDrive-me@x.com/My Drive", "Library/CloudStorage/Dropbox"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Mkdir(filepath.Join(vols, "Backup Disk"), 0o755)
	_ = os.Symlink("/", filepath.Join(vols, "Macintosh HD"))
	var repos []string
	for _, c := range storageChoices(home, vols) {
		repos = append(repos, strings.TrimPrefix(tildePath(c.Repo, home), vols))
	}
	want := []string{
		"~/Library/Mobile Documents/com~apple~CloudDocs/TelegramBackup", "rclone:gdrive:TelegramBackup",
		"~/Library/CloudStorage/Dropbox/TelegramBackup", "~/Library/CloudStorage/GoogleDrive-me@x.com/My Drive/TelegramBackup",
		"/Backup Disk/TelegramBackup", "",
	}
	if !slices.Equal(repos, want) {
		t.Errorf("repos = %q", repos)
	}
}

func TestOffDisk(t *testing.T) {
	home := "/Users/me"
	for repo, want := range map[string]bool{
		"~/TelegramBackup": false, "/Users/me/x": false, "/opt/backup": false,
		"/Volumes/Disk/TelegramBackup": true, "~/Library/Mobile Documents/com~apple~CloudDocs/b": true,
		"~/Library/CloudStorage/Dropbox/b": true, "rclone:gdrive:b": true, "sftp:nas:/b": true,
	} {
		if got := offDisk(repo, home); got != want {
			t.Errorf("%s: %v", repo, got)
		}
	}
}

func backupConfig(t *testing.T, p config.Paths) ([]string, config.Secrets) {
	t.Helper()
	cfg, err := config.LoadWithoutTargets(p.Config, p.Home)
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := config.LoadSecrets(p.Secrets, func(string) string { return "" })
	return cfg.Backup.Repositories, sec
}

// Enter at every question: a folder on this Mac, no extra places, a
// generated password that is shown and must be confirmed as saved.
func TestBackupDefaultsToAFolderOnThisMac(t *testing.T) {
	w, out, p := testWizard(t, "\n\n\n\nok\nsaved\n")
	inited := false
	w.initRepos = func(context.Context) error { inited = true; return nil }
	if err := w.setBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	repos, sec := backupConfig(t, p)
	if !slices.Equal(repos, []string{filepath.Join(p.Home, "TelegramBackup")}) || len(sec.ResticPassword) != 35 || !inited {
		t.Fatalf("repos %q, password %d chars, inited %v\n%s", repos, len(sec.ResticPassword), inited, out)
	}
	if !strings.Contains(out.String(), sec.ResticPassword) || strings.Count(out.String(), `then type "saved"`) != 2 {
		t.Errorf("the password must be shown and confirmed as saved:\n%s", out)
	}
}

func TestBackupPasswordIsNeverSetSilently(t *testing.T) {
	w, _, p := testWizard(t, "\n\n\n\n") // Enter everywhere, then input ends at "saved"
	if err := w.setBackup(context.Background()); !errors.Is(err, errStopped) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p.Secrets); err == nil {
		t.Fatal("a password nobody confirmed was written")
	}
	if _, err := os.Stat(p.Config); err == nil {
		t.Fatal("backup places written before the password was settled")
	}
}

func TestBackupChangeKeepsThePasswordAndAddsPlaces(t *testing.T) {
	w, out, p := testWizard(t, "\n\n\n\nsaved\n")
	if err := w.setBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, sec := backupConfig(t, p)
	// The fake initRepos creates nothing; the folder repository must look created to count as done.
	if err := os.MkdirAll(filepath.Join(p.Home, "TelegramBackup"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(p.Home, "TelegramBackup", "config"), nil, 0o600)
	w.in = newLineReader(strings.NewReader("y\n\n\n1\n\n"), nil) // change, keep backing up, keep folder, add Google Drive, keep password
	if err := runStep(context.Background(), w, initStep{name: "Backups", check: w.checkStorage, change: "Change?", run: w.setBackup}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	repos, sec2 := backupConfig(t, p)
	if !slices.Equal(repos, []string{filepath.Join(p.Home, "TelegramBackup"), "rclone:gdrive:TelegramBackup"}) || sec2.ResticPassword != sec.ResticPassword {
		t.Errorf("repos = %q, password kept %v", repos, sec2.ResticPassword == sec.ResticPassword)
	}
}

func TestBackupCanBeDeclinedAndRejectsFoldersInTheArchive(t *testing.T) {
	w, _, p := testWizard(t, "n\n")
	if err := w.setBackup(context.Background()); !errors.Is(err, errDeclined) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p.Config); err == nil {
		t.Error("declining wrote the config")
	}
	w, out, _ := testWizard(t, "\n~/TelegramArchive/backup\nnone\n0\n")
	if err := w.setBackup(context.Background()); err == nil || !strings.Contains(out.String(), "apart from the archive") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestBackupsCanBeTurnedOff(t *testing.T) {
	w, _, p := testWizard(t, "\n\n\n\nsaved\n")
	if err := w.setBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.in = newLineReader(strings.NewReader("n\n"), nil)
	if err := w.setBackup(context.Background()); !errors.Is(err, errDeclined) {
		t.Fatalf("err = %v", err)
	}
	if repos, _ := backupConfig(t, p); len(repos) != 0 {
		t.Fatalf("repos = %q", repos)
	}
}

// A config restored on a new Mac lists the places but secrets.env has no
// password: the step is not done, and the old password can be typed in.
func TestRestoredConfigAsksForTheExistingPassword(t *testing.T) {
	w, out, p := testWizard(t, "\n\n\nn\nthe old password 1\nthe old password 1\nsaved\n")
	if err := config.SetValue(p.Config, []string{"backup", "repositories"}, []string{"~/TelegramBackup"}); err != nil {
		t.Fatal(err)
	}
	if _, done, _ := w.checkStorage(context.Background()); done {
		t.Fatal("places without a password counted as done")
	}
	if err := w.setBackup(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, sec := backupConfig(t, p); sec.ResticPassword != "the old password 1" {
		t.Fatalf("password = %q", sec.ResticPassword)
	}
}

func TestBackupFolderNotCreatedIsNotDone(t *testing.T) {
	w, _, p := testWizard(t, "\n\n\n\nsaved\n")
	if err := w.setBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, done, _ := w.checkStorage(context.Background()); done || !strings.Contains(status, "not created yet") {
		t.Fatalf("status %q done %v", status, done)
	}
	_ = os.MkdirAll(filepath.Join(p.Home, "TelegramBackup"), 0o700)
	_ = os.WriteFile(filepath.Join(p.Home, "TelegramBackup", "config"), nil, 0o600)
	if _, done, _ := w.checkStorage(context.Background()); !done {
		t.Fatal("created repository not seen")
	}
}
