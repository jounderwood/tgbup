package main

import (
	"gitlab.com/joeunderwood/tgbup/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestDCUsesSeparateSession(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "tgbup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("TG_API_ID=1\nTG_API_HASH=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home}
	e := Env{Getenv: func(k string) string { return env[k] }}

	prod, err := loadSetup(e, false)
	if err != nil {
		t.Fatal(err)
	}
	if o := prod.clientOptions(); o.TestDC || filepath.Base(o.SessionPath) != "session" {
		t.Fatalf("prod options = %+v", o)
	}

	env["TGBUP_TEST_DC"] = "1"
	// The default config is the real archive: test servers need their own.
	if _, err := loadSetup(e, false); err == nil || !strings.Contains(err.Error(), "TGBUP_CONFIG") {
		t.Fatalf("test servers with the real config: %v", err)
	}
	if _, err := resolveSetup(e, false); err != nil {
		t.Fatalf("login (session only) on test servers: %v", err)
	}
	env["TGBUP_CONFIG"] = filepath.Join(home, "test", "config.yaml")
	if _, err := loadSetup(e, false); err == nil || !strings.Contains(err.Error(), "TGBUP_SECRETS") {
		t.Fatalf("test servers with the real secrets: %v", err)
	}
	env["TGBUP_SECRETS"] = filepath.Join(dir, "secrets.env") // read-only here; a real run uses a copy
	env["TGBUP_STATE_DIR"] = filepath.Join(home, "test", "state")
	test, err := loadSetup(e, false)
	if err != nil {
		t.Fatal(err)
	}
	// Before the test config exists, the default archive is next to it, never the
	// real one, and it is written down at once: a later write of another setting
	// would otherwise start from the template with the real ~/Tgbup/archive.
	want := filepath.Join(home, "test", "archive")
	if test.cfg.ArchiveDir != want {
		t.Errorf("test-server archive = %s, want %s", test.cfg.ArchiveDir, want)
	}
	if err := config.SetValue(env["TGBUP_CONFIG"], []string{"backup", "repositories"}, []string{"/r"}); err != nil {
		t.Fatal(err)
	}
	if again, err := loadSetup(e, false); err != nil || again.cfg.ArchiveDir != want {
		t.Errorf("after saving another setting the archive is %s (%v)", again.cfg.ArchiveDir, err)
	}
	if o := test.clientOptions(); !o.TestDC || filepath.Base(o.SessionPath) != "session-test" {
		t.Fatalf("test-DC options = %+v", o)
	}
}

// setup exists to fill in targets, so a config without them must not stop it.
func TestSetupLoadsConfigWithoutTargets(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "tgbup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("TG_API_ID=1\nTG_API_HASH=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("archive_dir: /mnt/nas/tg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := Env{Getenv: func(k string) string { return map[string]string{"HOME": home}[k] }}

	st, err := loadSetup(e, false)
	if err != nil || st.cfg.ArchiveDir != "/mnt/nas/tg" {
		t.Fatalf("setup = %+v, %v", st.cfg, err)
	}
	if _, err := loadSetup(e, true); err == nil {
		t.Fatal("sync accepted a config without targets")
	}
}
