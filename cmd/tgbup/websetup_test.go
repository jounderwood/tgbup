package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tgerr"

	"gitlab.com/joeunderwood/tgbup/internal/serve"
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
	// A generated password is kept only once the person said it is saved.
	body := `{"enabled":true,"local":"~/TelegramBackup","password":"` + pw + `","password_saved":false}`
	if _, err := b.SetupAction(ctx, "backup", []byte(body)); err == nil || !strings.Contains(err.Error(), "tick the box") {
		t.Fatalf("unsaved password: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "tgbup", "secrets.env")); err != nil {
		t.Fatal(err)
	}
	if sec := readSecrets(t, home); sec != "" && strings.Contains(sec, pw) {
		t.Fatal("password written before it was confirmed")
	}
	if _, err := b.SetupAction(ctx, "backup", []byte(`{"enabled":true,"local":"~/Arch/inside","password":"`+pw+`","password_saved":true}`)); !errors.Is(err, serve.ErrBadRequest) {
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
