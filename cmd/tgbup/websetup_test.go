package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/joeunderwood/tgbup/internal/config"
	"github.com/joeunderwood/tgbup/internal/serve"
)

func TestLoginErrorsAreActionable(t *testing.T) {
	for err, want := range map[error]string{
		tgerr.New(400, "PHONE_CODE_INVALID"):    "wrong code",
		tgerr.New(400, "PHONE_CODE_EXPIRED"):    "send a new one",
		tgerr.New(400, "PASSWORD_HASH_INVALID"): "wrong password",
		tgerr.New(400, "PHONE_NUMBER_INVALID"):  "international format",
		tgerr.New(420, "FLOOD_WAIT_60"):         "wait 1m0s",
	} {
		got := loginError(err)
		if !strings.Contains(got.Error(), want) || !errors.Is(got, serve.ErrBadRequest) {
			t.Errorf("%v → %v, want %q as bad input", err, got, want)
		}
	}
}

// webBackend runs the setup actions against files in a temp home.
func webBackend(t *testing.T) (*serveBackend, string) {
	t.Helper()
	w, _, p := testWizard(t, "")
	// loggedIn skips asking Telegram whether the session works: tests stay offline.
	return &serveBackend{env: w.env, loggedIn: true}, p.Home
}

func TestWebSetupKeysArchiveAndBackupPassword(t *testing.T) {
	b, home := webBackend(t)
	ctx := context.Background()
	if _, err := b.SetupAction(ctx, "keys", []byte(`{"api_id":"x","api_hash":"y"}`)); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("bad keys: %v", err)
	}
	if _, err := b.SetupAction(ctx, "keys", []byte(`{"api_id":"12345","api_hash":"0123456789abcdef0123456789abcdef"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetupAction(ctx, "archive", []byte(`{"dir":"relative"}`)); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("relative archive dir: %v", err)
	}
	if _, err := b.SetupAction(ctx, "archive", []byte(`{"dir":"~/Arch"}`)); err != nil {
		t.Fatal(err)
	}
	res, err := b.SetupAction(ctx, "password/new", nil)
	if err != nil {
		t.Fatal(err)
	}
	pw := res.(map[string]string)["password"]
	// Generating only shows a password; it is kept when the form with it is saved.
	if sec := readSecrets(t, home); strings.Contains(sec, pw) {
		t.Fatal("generated password written before the form was saved")
	}
	if _, err := b.SetupAction(ctx, "backup", []byte(`{"enabled":true,"local":"~/TelegramBackup"}`)); err == nil || !strings.Contains(err.Error(), "set the backup password") {
		t.Fatalf("backup without a password: %v", err)
	}
	if _, err := b.SetupAction(ctx, "backup", []byte(`{"enabled":true,"local":"~/Arch/inside","password":"`+pw+`"}`)); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("backup inside the archive: %v", err)
	}
	if _, err := b.SetupAction(ctx, "nope", nil); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("unknown action: %v", err)
	}
	st, err := b.SetupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := st.(setupState)
	if s.Steps[0].ID != "keys" || !s.Steps[0].Done || s.Archive.Dir != "~/Arch" || s.Complete {
		t.Errorf("state = %+v", s)
	}
}

func readSecrets(t *testing.T, home string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(home, ".config", "tgbup", "secrets.env"))
	return string(b)
}

// fakeAuth plays Telegram's side of a sign-in: code 12345, and with
// twoFA, password "secret".
type fakeAuth struct {
	twoFA  bool
	closed *int
}

func (f fakeAuth) SendCode(context.Context, string, auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	return &tg.AuthSentCode{PhoneCodeHash: "h"}, nil
}

func (f fakeAuth) SignIn(_ context.Context, _, code, hash string) (*tg.AuthAuthorization, error) {
	switch {
	case hash != "h" || code != "12345":
		return nil, tgerr.New(400, "PHONE_CODE_INVALID")
	case f.twoFA:
		return nil, auth.ErrPasswordAuthNeeded
	}
	return &tg.AuthAuthorization{}, nil
}

func (f fakeAuth) Password(_ context.Context, pw string) (*tg.AuthAuthorization, error) {
	if pw != "secret" {
		return nil, tgerr.New(400, "PASSWORD_HASH_INVALID")
	}
	return &tg.AuthAuthorization{}, nil
}

func TestWebLoginStepByStep(t *testing.T) {
	b, _ := webBackend(t)
	b.loggedIn = false
	closed := 0
	b.dialLogin = func(context.Context, setup) (authFlow, func(), error) {
		return fakeAuth{twoFA: true, closed: &closed}, func() { closed++ }, nil
	}
	ctx := context.Background()
	act := func(name, body string) (any, error) { return b.SetupAction(ctx, name, []byte(body)) }

	if _, err := act("login/signin", `{"code":"12345"}`); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("sign in before a code was sent: %v", err)
	}
	if _, err := act("login/code", `{"phone":"79990001122"}`); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("phone without +: %v", err)
	}
	if _, err := act("login/code", `{"phone":"+7 999 000 11 22"}`); err != nil {
		t.Fatal(err)
	}
	// A second "Send code" closes the first connection instead of leaking it.
	if _, err := act("login/code", `{"phone":"+79990001122"}`); err != nil || closed != 1 {
		t.Fatalf("second code: %v, closed %d", err, closed)
	}
	if _, err := act("login/signin", `{"code":"00000"}`); err == nil || !strings.Contains(err.Error(), "wrong code") {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := act("login/password", `{"password":"secret"}`); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("password before the code: %v", err)
	}
	res, err := act("login/signin", `{"code":"12345"}`)
	if err != nil || !res.(map[string]bool)["password_needed"] {
		t.Fatalf("code: %v %v", res, err)
	}
	if status, done, _ := b.checkLoginCached(ctx); done || status != "signing in…" {
		t.Fatalf("while signing in: %q %v (must not open a second client)", status, done)
	}
	if _, err := act("login/password", `{"password":"nope"}`); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("wrong password: %v", err)
	}
	if res, err := act("login/password", `{"password":"secret"}`); err != nil || !res.(map[string]bool)["done"] {
		t.Fatalf("password: %v %v", res, err)
	}
	if closed != 2 || b.login != nil || !b.loggedIn {
		t.Fatalf("after sign-in: closed %d, login %v, loggedIn %v", closed, b.login, b.loggedIn)
	}
	// New keys may belong to another app: the session is not assumed to work.
	if _, err := act("keys", `{"api_id":"12345","api_hash":"0123456789abcdef0123456789abcdef"}`); err != nil || b.loggedIn {
		t.Fatalf("keys: %v, loggedIn %v", err, b.loggedIn)
	}
}

func TestAdoptRestoredConfig(t *testing.T) {
	w, _, p := testWizard(t, "")
	snap := []byte("archive_dir: ~/Old\ntargets:\n  include: [10] # Mom\nbackup:\n  repositories: [~/TelegramBackup]\ndeps:\n  restic: /tmp/evil-restic\n")
	// A config with backups set first and no chats: the snapshot's is taken, places from both.
	if err := config.SetValue(p.Config, []string{"backup", "repositories"}, []string{"rclone:gdrive:TelegramBackup"}); err != nil {
		t.Fatal(err)
	}
	st, err := w.load()
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := adoptRestoredConfig(st, snap, filepath.Join(p.Home, "New"), filepath.Join(p.Home, "Ext"))
	if err != nil || !adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	cfg, err := config.Load(p.Config, p.Home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArchiveDir != filepath.Join(p.Home, "New") || len(cfg.Targets.Include) != 1 ||
		!slices.Equal(cfg.Backup.Repositories, []string{filepath.Join(p.Home, "TelegramBackup"), "rclone:gdrive:TelegramBackup", filepath.Join(p.Home, "Ext")}) {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Deps.Restic != "" {
		t.Errorf("a snapshot chose the restic to run: %q", cfg.Deps.Restic)
	}
	// A config with chats keeps its archive folder: restoring elsewhere only to look must not move it.
	if st, err = w.load(); err != nil {
		t.Fatal(err)
	}
	if adopted, err := adoptRestoredConfig(st, snap, filepath.Join(p.Home, "look"), ""); err != nil || adopted {
		t.Fatalf("second adopt %v, %v", adopted, err)
	}
	if cfg, _ := config.Load(p.Config, p.Home); cfg.ArchiveDir != filepath.Join(p.Home, "New") {
		t.Fatalf("archive_dir moved to %s", cfg.ArchiveDir)
	}
}

// The page saves the times and installs the job again with them, or turns it off.
func TestWebScheduleTimesAndOff(t *testing.T) {
	b, home := webBackend(t)
	ctx := context.Background()
	fake := &fakeLaunchctl{}
	var installed launchdJob
	b.newJob = func(st setup) (launchdJob, error) {
		j, _ := testSchedule(t)
		j.Exe, j.AgentsDir, j.Times = "/usr/local/bin/tgbup", filepath.Join(home, "LaunchAgents"), st.cfg.Schedule.Clocks()
		j.launchctl = fake.run
		installed = j
		return j, nil
	}
	if _, err := b.SetupAction(ctx, "archive", []byte(`{"dir":"~/Arch"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetupAction(ctx, "schedule/install", []byte(`{"times":"25:00"}`)); !errors.Is(err, serve.ErrBadRequest) {
		t.Fatalf("bad time: %v", err)
	}
	if _, err := b.SetupAction(ctx, "schedule/install", []byte(`{"times":"19:40, 7:05"}`)); err != nil {
		t.Fatal(err)
	}
	if got := clockList(installed.Times); got != "07:05, 19:40" {
		t.Errorf("installed with %s", got)
	}
	st, err := b.load()
	if err != nil || clockList(st.cfg.Schedule.Clocks()) != "07:05, 19:40" {
		t.Errorf("config times = %v %v", st.cfg.Schedule.Times, err)
	}
	if !strings.Contains(strings.Join(fake.calls, "\n"), "bootstrap") {
		t.Errorf("not installed: %q", fake.calls)
	}
	if _, err := b.SetupAction(ctx, "schedule/off", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed.path()); err == nil {
		t.Error("turned off, but the job file stays")
	}
}

func TestWizardAsksForTimes(t *testing.T) {
	w, out, _ := testWizard(t, "25:00\n7:05 19:40\n")
	j, changed, err := w.askTimes(context.Background(), launchdJob{})
	if err != nil || !changed || clockList(j.Times) != "07:05, 19:40" {
		t.Fatalf("times = %v %v %v\n%s", j.Times, changed, err, out)
	}
	if !strings.Contains(out.String(), "not a time") {
		t.Errorf("no explanation:\n%s", out)
	}
	w.in = newLineReader(strings.NewReader("\n"), nil)
	if j, changed, err := w.askTimes(context.Background(), j); err != nil || changed || clockList(j.Times) != "07:05, 19:40" {
		t.Fatalf("Enter changed the times: %v %v %v", j.Times, changed, err)
	}
}

// On Telegram's test servers the step never reaches launchd: the job is the
// real archive's.
func TestScheduleStepLeavesTheJobAloneInTestMode(t *testing.T) {
	dir := t.TempDir()
	vars := map[string]string{"HOME": dir, "TGBUP_TEST_DC": "1", "TGBUP_CONFIG": filepath.Join(dir, "t", "config.yaml"),
		"TGBUP_SECRETS": filepath.Join(dir, "t", "secrets.env"), "TGBUP_STATE_DIR": filepath.Join(dir, "t", "state")}
	var out bytes.Buffer
	env := Env{Stdin: strings.NewReader("y\ny\n7:00\ny\n"), Stdout: &out, Stderr: &out, Getenv: func(k string) string { return vars[k] }}
	w := &wizard{env: env, out: &out, in: newLineReader(env.Stdin, nil)}
	if err := w.schedule(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "?") {
		t.Errorf("asked about the real job:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "Library", "LaunchAgents")); err == nil {
		t.Error("a launchd job was written")
	}
}
