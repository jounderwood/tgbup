package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// exportEnv: a forum "Family" (General in Feb, Expenses in Mar with a document),
// a private chat "Mom", and an unrelated group also titled "Twins" twice.
func exportEnv(t *testing.T) (Env, string, *bytes.Buffer) {
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
	write(filepath.Join(archive, "media", "document", "9.pdf"), "pdf-bytes")

	db, err := store.Open(filepath.Join(archive, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, c := range []store.Chat{
		{ID: -1001, Type: store.ChatForum, Title: "Family", Username: "famforum"},
		{ID: 10, Type: store.ChatPrivate, Title: "Mom"},
		{ID: -20, Type: store.ChatGroup, Title: "Twins"},
		{ID: -21, Type: store.ChatGroup, Title: "twins"},
	} {
		if err := db.UpsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertTopics(ctx, []store.Topic{{ChatID: -1001, TopicID: 1, Title: "General"}, {ChatID: -1001, TopicID: 160, Title: "Expenses"}}); err != nil {
		t.Fatal(err)
	}
	feb := time.Date(2026, 2, 10, 12, 0, 0, 0, time.Local).Unix()
	mar := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local).Unix()
	for _, b := range []store.Batch{
		{ChatID: -1001, Media: []store.Media{{Key: "document:9", Kind: "document", FileName: "lease.pdf", Size: 9}}, Messages: []store.Message{
			{ChatID: -1001, ID: 1, TopicID: 1, Date: feb, Text: "hello", Raw: []byte{1}},
			{ChatID: -1001, ID: 2, TopicID: 160, Date: mar, Text: "rent", ReplyToID: 1, Raw: []byte{1}, MediaKeys: []string{"document:9"}},
		}},
		{ChatID: 10, Messages: []store.Message{{ChatID: 10, ID: 1, Date: mar, Text: "hi mom", Raw: []byte{1}}}},
	} {
		if err := db.SaveBatch(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MediaDone(ctx, "document:9", "media/document/9.pdf"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	env := Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out,
		Getenv: func(k string) string { return map[string]string{"HOME": home}[k] }}
	return env, home, &out
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	return names
}

func has(names []string, suffix string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, suffix) })
}

func TestExportChatWithMedia(t *testing.T) {
	env, home, out := exportEnv(t)
	zipPath := filepath.Join(home, "family.zip")
	if code := run(context.Background(), []string{"export", "@famforum", "-o", zipPath}, env); code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	names := zipNames(t, zipPath)
	for _, want := range []string{"family/index.html", "family/html/index.html", "family/html/Family/General/2026-02.html",
		"family/html/Family/Expenses/2026-03.html", "family/md/Family/Expenses/2026-03.md", "family/media/document/9.pdf"} {
		if !slices.Contains(names, want) {
			t.Errorf("zip lacks %s; has %v", want, names)
		}
	}
	if has(names, "Mom/2026-03.html") {
		t.Errorf("another chat leaked into the export: %v", names)
	}
	if !strings.Contains(out.String(), "2 messages, 1 files") {
		t.Errorf("summary = %q", out.String())
	}
}

func TestExportTopicPeriodWithoutMedia(t *testing.T) {
	env, home, out := exportEnv(t)
	zipPath := filepath.Join(home, "x.zip")
	args := []string{"export", "family", "--topic", "expenses", "--from", "2026-03", "--to", "2026-03", "--no-media", "-o", zipPath}
	if code := run(context.Background(), args, env); code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	names := zipNames(t, zipPath)
	if !has(names, "html/Family/Expenses/2026-03.html") || has(names, "General/2026-02.html") || has(names, "9.pdf") {
		t.Fatalf("zip = %v", names)
	}
	r, _ := zip.OpenReader(zipPath)
	defer r.Close()
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "Expenses/2026-03.html") {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			_, err = b.ReadFrom(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			// The replied-to message is outside the export: quoted, not linked.
			if strings.Contains(b.String(), "2026-02.html") || !strings.Contains(b.String(), "hello") {
				t.Errorf("reply outside the export:\n%s", b.String())
			}
		}
	}
}

func TestExportRejectsUnknownOrAmbiguousChat(t *testing.T) {
	env, home, out := exportEnv(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"export", "Twins", "-o", filepath.Join(home, "a.zip")}, "-21, -20"},
		{[]string{"export", "Nobody", "-o", filepath.Join(home, "a.zip")}, "no archived chat"},
		{[]string{"export", "Family", "--topic", "Nope", "-o", filepath.Join(home, "a.zip")}, "no topic"},
		{[]string{"export", "Family"}, "-o"},
		{[]string{"export", "Family", "--from", "March", "-o", filepath.Join(home, "a.zip")}, "YYYY-MM"},
	} {
		out.Reset()
		if code := run(context.Background(), c.args, env); code != 2 || !strings.Contains(out.String(), c.want) {
			t.Errorf("%v: code %d, output %q (want %q)", c.args, code, out.String(), c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "a.zip")); !os.IsNotExist(err) {
		t.Error("a failed export left a zip behind")
	}
}
