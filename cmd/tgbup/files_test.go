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
