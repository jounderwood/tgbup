package names_test

import (
	"strings"
	"testing"

	"github.com/joeunderwood/tgbup/internal/names"
)

func TestCleanSanitizes(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":               "etcpasswd",
		`a:b*c?"d<e>f|g\h`:               "abcdefgh",
		"..hidden":                       "hidden",
		"bell\x07\nline":                 "bell line",
		"Договор  аренды":                "Договор аренды",
		strings.Repeat("я", 150):         strings.Repeat("я", 100),
		"...":                            "",
		"trailing. ":                     "trailing",
		"Mixed Case Stays":               "Mixed Case Stays",
		"\u202Egnp.exe":                  "gnp.exe", // right-to-left override would disguise the extension
		"zero\u200dwidth joiner is kept": "zero\u200dwidth joiner is kept",
	} {
		if got := names.Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanBytesCutsOnRuneBoundary(t *testing.T) {
	got := names.CleanBytes(strings.Repeat("я", 150), 201) // 2 bytes each
	if len(got) != 200 || !strings.HasPrefix(strings.Repeat("я", 150), got) {
		t.Fatalf("got %d bytes: %q", len(got), got)
	}
	if got := names.CleanBytes("short name", 200); got != "short name" {
		t.Fatalf("got %q", got)
	}
}

func TestFolderNamesSeparateSameTitles(t *testing.T) {
	got := names.FolderNames([]names.FolderChat{
		{ID: 10, Title: "Family"}, {ID: -20, Title: "family"}, {ID: 30, Title: "../Mom/"},
		{ID: -45, Title: "Old", UpgradedTo: -1001, UpgradedTitle: "Work"},
	})
	want := map[int64]string{10: "Family (10)", -20: "family (-20)", 30: "Mom", -1001: "Work"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("folder %d = %q, want %q", id, got[id], w)
		}
	}
}

// A contact may name themselves after one of the site's files, or in another
// Unicode normalization of a name already taken: neither may take its place.
func TestFolderNamesAvoidSiteFilesAndLookalikes(t *testing.T) {
	nfc, nfd := "Café", "Café"
	got := names.FolderNames([]names.FolderChat{{ID: 1, Title: "index.html"}, {ID: 2, Title: "Media"}, {ID: 3, Title: nfc}, {ID: 4, Title: nfd}, {ID: 5, Title: "Mom"}})
	for id, want := range map[int64]string{1: "index.html (1)", 2: "Media (2)", 3: nfc + " (3)", 4: nfd + " (4)", 5: "Mom"} {
		if got[id] != want {
			t.Errorf("chat %d: %q, want %q", id, got[id], want)
		}
	}
	for name, taken := range map[string]bool{"index.html": true, "P12.html": true, "dates.md": true, "nav.js": true, "p1x.html": false, "Recipes": false} {
		if names.ThreadFile(name) != taken {
			t.Errorf("ThreadFile(%q) = %v", name, !taken)
		}
	}
}
