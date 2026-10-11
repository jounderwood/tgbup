package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jounderwood/tgbup/internal/config"
)

// The explanations name this computer's files and commands to check them,
// and never a secret's value.
func TestHowItWorksNamesFilesNotSecrets(t *testing.T) {
	w, _, p := testWizard(t, "")
	ctx := context.Background()
	for path, v := range map[string]any{
		"backup.repositories": []string{filepath.Join(p.Home, "Tgbup", "backup"), "rest:https://u:placepass@host/r"},
	} {
		if err := config.SetValue(p.Config, strings.Split(path, "."), v); err != nil {
			t.Fatal(err)
		}
	}
	secrets := map[string]string{"API_ID": "123456", "API_HASH": "0123456789abcdef0123456789abcdef",
		"RESTIC_PASSWORD": "a very secret password 1", "BOT_TOKEN": "111:AAbotTokenValue", "BOT_CHAT_ID": "98765"}
	if err := config.SetSecrets(p.Secrets, secrets); err != nil {
		t.Fatal(err)
	}
	st, err := loadLocalSetup(w.env, false)
	if err != nil {
		t.Fatal(err)
	}
	hows := howItWorks(ctx, st, w.env)
	all, _ := json.Marshal(hows)
	for k, v := range secrets {
		if k != "API_ID" && strings.Contains(string(all), v) {
			t.Errorf("the explanations show %s", k)
		}
	}
	if strings.Contains(string(all), "placepass") {
		t.Error("the explanations show a password inside a place address")
	}
	for _, id := range []string{"keys", "login", "archive", "backup", "bot"} {
		h := hows[id]
		if h == nil || len(h.What) == 0 || len(h.Check) == 0 {
			t.Fatalf("%s: %+v", id, h)
		}
	}
	text := func(id string) string { b, _ := json.Marshal(hows[id]); return string(b) }
	for id, want := range map[string]string{
		"keys":    "~/.local/state/tgbup/secrets.env",
		"login":   "~/.local/state/tgbup/session",
		"backup":  `snapshots --tag tgbup`,
		"archive": "ls -ld ~/Tgbup/archive",
		"bot":     "tgbup notify test",
	} {
		if !strings.Contains(text(id), want) {
			t.Errorf("%s lacks %q: %s", id, want, text(id))
		}
	}
	if !strings.Contains(text("backup"), `sed -n 's/^RESTIC_PASSWORD=//p' ~/.local/state/tgbup/secrets.env`) {
		t.Errorf("the restic command does not read the password from the file: %s", text("backup"))
	}
}
