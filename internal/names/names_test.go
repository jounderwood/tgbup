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
