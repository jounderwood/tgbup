package main

import (
	"os"
	"path/filepath"
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
	test, err := loadSetup(e, false)
	if err != nil {
		t.Fatal(err)
	}
	if o := test.clientOptions(); !o.TestDC || filepath.Base(o.SessionPath) != "session-test" {
		t.Fatalf("test-DC options = %+v", o)
	}
}
