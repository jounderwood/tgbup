package browse_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/browse"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

// March 14 2024 12:30 UTC.
const march14 = int64(1710419400)

func archive(t *testing.T, paths ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(dir, "files")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildLayout(t *testing.T) {
	dir := archive(t, "media/document/9.pdf", "media/photo/3.jpg", "media/voice/4.ogg")
	files := []store.DownloadedFile{
		{LocalPath: "media/document/9.pdf", Kind: "document", FileName: "Договор аренды.pdf", Date: march14,
			ChatID: -1001, ChatTitle: "Family", ChatType: store.ChatForum, TopicID: 160, TopicTitle: "Expenses"},
		{LocalPath: "media/photo/3.jpg", Kind: "photo", Date: march14 + 60,
			ChatID: -1001, ChatTitle: "Family", ChatType: store.ChatForum, TopicID: 1, TopicTitle: "General"},
		{LocalPath: "media/photo/3.jpg", Kind: "photo", Date: march14 - 86400,
			ChatID: -45, ChatTitle: "Family", ChatType: store.ChatGroup, UpgradedTo: -1001, UpgradedTitle: "Family"},
		{LocalPath: "media/voice/4.ogg", Kind: "voice", Date: march14,
			ChatID: 10, ChatTitle: "Мама", ChatType: store.ChatPrivate},
	}
	res, err := browse.Build(dir, files, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Family/Expenses/2024-03-14 12.30 Договор аренды.pdf",
		"Family/General/2024-03-14 12.31 photo.jpg",
		"Family/before upgrade/2024-03-13 12.30 photo.jpg",
		"Мама/2024-03-14 12.30 voice.ogg",
	}
	got := listing(t, dir)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("files =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.Linked != 4 || res.Missing != 0 {
		t.Errorf("result = %+v", res)
	}
	a, _ := os.Stat(filepath.Join(dir, "media/document/9.pdf"))
	b, _ := os.Stat(filepath.Join(dir, "files", want[0]))
	if !os.SameFile(a, b) {
		t.Error("files/ entry is not a hard link to media/")
	}
}

func TestBuildSanitizesNames(t *testing.T) {
	dir := archive(t, "media/document/1.pdf")
	names := map[string]string{
		"../../etc/passwd.pdf":            "etcpasswd.pdf",
		`a:b*c?"d<e>f|g\h.pdf`:            "abcdefgh.pdf",
		"..hidden.pdf":                    "hidden.pdf",
		"bell\x07\nline.pdf":              "bell line.pdf",
		strings.Repeat("я", 150) + ".pdf": strings.Repeat("я", 100) + ".pdf",
		"...":                             "file.pdf",
	}
	for name, want := range names {
		_, err := browse.Build(dir, []store.DownloadedFile{{LocalPath: "media/document/1.pdf", Kind: "document",
			FileName: name, Date: march14, ChatID: 10, ChatTitle: "../Mom/", ChatType: store.ChatPrivate}}, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if got := listing(t, dir); len(got) != 1 || got[0] != "Mom/2024-03-14 12.30 "+want {
			t.Errorf("%q → %v, want Mom/2024-03-14 12.30 %s", name, got, want)
		}
	}
}

func TestBuildResolvesCollisions(t *testing.T) {
	dir := archive(t, "media/photo/1.jpg", "media/photo/2.jpg", "media/photo/3.jpg")
	files := []store.DownloadedFile{
		{LocalPath: "media/photo/1.jpg", Kind: "photo", Date: march14, ChatID: 10, ChatTitle: "Family", ChatType: store.ChatPrivate},
		{LocalPath: "media/photo/2.jpg", Kind: "photo", Date: march14 + 5, ChatID: 10, ChatTitle: "Family", ChatType: store.ChatPrivate},
		// A different chat with the same title gets its id in the folder name.
		{LocalPath: "media/photo/3.jpg", Kind: "photo", Date: march14, ChatID: -20, ChatTitle: "Family", ChatType: store.ChatGroup},
	}
	if _, err := browse.Build(dir, files, time.UTC); err != nil {
		t.Fatal(err)
	}
	got := listing(t, dir)
	slices.Sort(got)
	want := []string{
		"Family (-20)/2024-03-14 12.30 photo.jpg",
		"Family (10)/2024-03-14 12.30 photo (2).jpg",
		"Family (10)/2024-03-14 12.30 photo.jpg",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("files = %q", got)
	}
}

func TestRebuildReplacesTheOldView(t *testing.T) {
	dir := archive(t, "media/photo/1.jpg", "files/Renamed chat/old.jpg", ".files-new/leftover.jpg")
	res, err := browse.Build(dir, []store.DownloadedFile{
		{LocalPath: "media/photo/1.jpg", Kind: "photo", Date: march14, ChatID: 10, ChatTitle: "Mom", ChatType: store.ChatPrivate},
		{LocalPath: "media/photo/gone.jpg", Kind: "photo", Date: march14, ChatID: 10, ChatTitle: "Mom", ChatType: store.ChatPrivate},
	}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := listing(t, dir); !slices.Equal(got, []string{"Mom/2024-03-14 12.30 photo.jpg"}) {
		t.Fatalf("files = %q", got)
	}
	if res.Missing != 1 {
		t.Errorf("missing = %d, want 1", res.Missing)
	}
	for _, leftover := range []string{".files-new", ".files-old"} {
		if _, err := os.Stat(filepath.Join(dir, leftover)); !os.IsNotExist(err) {
			t.Errorf("%s left behind", leftover)
		}
	}
}
