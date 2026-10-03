package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

func TestLoadSecretsPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "secrets.env", "# comment\nTG_API_ID=111\nTG_API_HASH=\"from-file\"\nBOT_TOKEN=file-token\n", 0o600)
	tokenFile := writeFile(t, dir, "token", "file-ref-token\n", 0o600)
	env := map[string]string{"TG_API_HASH": "from-env", "BOT_TOKEN_FILE": tokenFile}

	s, err := config.LoadSecrets(file, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if s.APIID != 111 || s.APIHash != "from-env" || s.BotToken != "file-ref-token" {
		t.Fatalf("secrets = %+v", s)
	}
	if err := s.RequireTelegram(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSecretsRejectsWorldReadableFile(t *testing.T) {
	file := writeFile(t, t.TempDir(), "secrets.env", "TG_API_ID=1\n", 0o644)
	_, err := config.LoadSecrets(file, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSecretsMissingFileIsFine(t *testing.T) {
	s, err := config.LoadSecrets(filepath.Join(t.TempDir(), "none"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequireTelegram(); err == nil {
		t.Fatal("RequireTelegram passed without credentials")
	}
}

func TestLoadSecretsBadAPIID(t *testing.T) {
	file := writeFile(t, t.TempDir(), "secrets.env", "TG_API_ID=abc\n", 0o600)
	if _, err := config.LoadSecrets(file, func(string) string { return "" }); err == nil {
		t.Fatal("non-numeric TG_API_ID accepted")
	}
}

func TestSetSecretsReplacesAppendsAndKeepsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.env")
	if err := os.WriteFile(path, []byte("# telegram\nTG_API_ID=1\nTG_API_HASH=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SetSecrets(path, map[string]string{"TG_API_ID": "42", "RESTIC_PASSWORD": "p w"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "# telegram\nTG_API_ID=42\nTG_API_HASH=abc\nRESTIC_PASSWORD=p w\n" {
		t.Errorf("secrets file:\n%s", raw)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	s, err := config.LoadSecrets(path, func(string) string { return "" })
	if err != nil || s.APIID != 42 || s.ResticPassword != "p w" {
		t.Errorf("loaded %+v, %v", s, err)
	}
}

func TestSetSecretsCreatesFileAndRejectsNewlines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secrets.env")
	if err := config.SetSecrets(path, map[string]string{"BOT_TOKEN": "1:x\nEVIL=1"}); err == nil {
		t.Fatal("value with a newline accepted")
	}
	if err := config.SetSecrets(path, map[string]string{"BOT_TOKEN": "1:x"}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", st, err)
	}
}
