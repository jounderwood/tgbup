package config_test

import (
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
