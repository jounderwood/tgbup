package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/browse"
	"gitlab.com/joeunderwood/tgbup/internal/render"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/syncer"
)

// filesEnv is a config + archive with one downloaded document, no Telegram needed.
func filesEnv(t *testing.T) (Env, string, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "tgbup")
	archive := filepath.Join(home, "archive")
	for _, d := range []string{cfgDir, filepath.Join(archive, "media", "document")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cfgDir, "secrets.env"), "TG_API_ID=1\nTG_API_HASH=x\n")
	write(filepath.Join(cfgDir, "config.yaml"), "archive_dir: "+archive+"\ntargets:\n  include: [10]\n")
	write(filepath.Join(archive, "media", "document", "9.pdf"), "pdf")

	db, err := store.Open(filepath.Join(archive, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.UpsertChat(ctx, store.Chat{ID: 10, Type: store.ChatPrivate, Title: "Mom"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBatch(ctx, store.Batch{ChatID: 10,
		Messages: []store.Message{{ChatID: 10, ID: 1, Date: 1710419400, Raw: []byte{1}, MediaKeys: []string{"document:9"}}},
		Media:    []store.Media{{Key: "document:9", Kind: "document", FileName: "Lease.pdf"}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.MediaDone(ctx, "document:9", "media/document/9.pdf"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	env := Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out,
		Getenv: func(k string) string { return map[string]string{"HOME": home}[k] }}
	return env, archive, &out
}

func TestFilesCommandBuildsTheView(t *testing.T) {
	env, archive, out := filesEnv(t)
	if code := run(context.Background(), []string{"files"}, env); code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	matches, _ := filepath.Glob(filepath.Join(archive, browse.Dir, "Mom", "* Lease.pdf"))
	if len(matches) != 1 {
		t.Fatalf("files/ view not built:\n%s", out)
	}
	if !strings.Contains(out.String(), "files_linked=1") {
		t.Errorf("output = %q", out.String())
	}
}

func TestFilesViewFailureFailsTheRun(t *testing.T) {
	sum := syncSummary{sync: syncer.Report{Chats: 1}, filesErr: errors.New("disk full")}
	if sum.ok() {
		t.Fatal("a failed files/ rebuild did not fail the run")
	}
	var out bytes.Buffer
	sum.print(&out)
	if !strings.Contains(out.String(), "files view: disk full") {
		t.Fatalf("summary = %q", out.String())
	}
}

func TestRenderCommandBuildsTheSite(t *testing.T) {
	env, archive, out := filesEnv(t)
	if code := run(context.Background(), []string{"render"}, env); code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	for _, p := range []string{"html/index.html", "md/index.md"} {
		if _, err := os.Stat(filepath.Join(archive, p)); err != nil {
			t.Errorf("missing %s:\n%s", p, out)
		}
	}
	if !strings.Contains(out.String(), "rendered=1 unchanged=0") {
		t.Errorf("output = %q", out.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"render", "--full"}, env); code != 0 || !strings.Contains(out.String(), "rendered=1") {
		t.Fatalf("--full: code %d, output %q", code, out.String())
	}
}

func TestRenderFailureFailsTheRun(t *testing.T) {
	sum := syncSummary{sync: syncer.Report{Chats: 1}, render: render.Result{Errors: []error{errors.New("page Mom/2026-03: disk full")}}}
	if sum.ok() {
		t.Fatal("a failed page did not fail the run")
	}
	var out bytes.Buffer
	sum.print(&out)
	if !strings.Contains(out.String(), "render: page Mom/2026-03: disk full") {
		t.Fatalf("summary = %q", out.String())
	}
}

// The backup command end to end, with a real restic (skipped without one).
func TestBackupCommand(t *testing.T) {
	restic := os.Getenv("TGBUP_TEST_RESTIC")
	if restic == "" {
		t.Skip("set TGBUP_TEST_RESTIC to a restic binary")
	}
	env, archive, out := filesEnv(t)
	home := filepath.Dir(archive)
	repo := filepath.Join(home, "repo")
	cfg := "archive_dir: " + archive + "\ntargets:\n  include: [10]\nbackup:\n  repositories: [" + repo + "]\ndeps:\n  restic: " + restic + "\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "tgbup", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(home, ".config", "tgbup", "secrets.env")
	if err := os.WriteFile(secrets, []byte("RESTIC_PASSWORD=pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), []string{"backup"}, env); code != 1 || !strings.Contains(out.String(), "repository does not exist") && !strings.Contains(out.String(), "backup --init") {
		t.Fatalf("backup before init: code %d\n%s", code, out)
	}
	out.Reset()
	if code := run(context.Background(), []string{"backup", "--init"}, env); code != 0 {
		t.Fatalf("init: code %d\n%s", code, out)
	}
	out.Reset()
	if code := run(context.Background(), []string{"backup"}, env); code != 0 || !strings.Contains(out.String(), "snapshot") {
		t.Fatalf("backup: code %d\n%s", code, out)
	}
}

func TestBackupNeedsAPassword(t *testing.T) {
	env, archive, out := filesEnv(t)
	home := filepath.Dir(archive)
	cfg := "archive_dir: " + archive + "\ntargets:\n  include: [10]\nbackup:\n  repositories: [" + filepath.Join(home, "repo") + "]\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "tgbup", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), []string{"backup"}, env); code != 1 || !strings.Contains(out.String(), "RESTIC_PASSWORD") {
		t.Fatalf("code %d\n%s", code, out)
	}
}
