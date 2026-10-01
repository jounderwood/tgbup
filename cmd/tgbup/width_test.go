package main

import "testing"

func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"Family":                6,
		"Мама":                  4,
		"Misha 米哈":              10,
		"Vetrova🌲":              9,
		"👨\u200d💻":              2,
		"🇷🇸":                    2,
		"⚡":                     2,
		"❤\ufe0f":               2,
		"é":                     1, // e + combining acute
		"семья 👨\u200d👩\u200d👧": 8,
	}
	for s, want := range cases {
		if got := displayWidth(s); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestPadRight(t *testing.T) {
	if got := padRight("米哈", 6); got != "米哈  " {
		t.Errorf("padRight = %q", got)
	}
	if got := padRight("long title", 4); got != "long title" {
		t.Errorf("padRight must not cut: %q", got)
	}
}

func TestCleanTitleCutsByWidth(t *testing.T) {
	if got := cleanTitle("米哈米哈米哈", 7); got != "米哈米…" {
		t.Errorf("cleanTitle = %q, want 6 cells + ellipsis", got)
	}
	// Never cut inside a ZWJ sequence: drop the whole emoji instead.
	if got := cleanTitle("abcd👨\u200d💻", 5); got != "abcd…" {
		t.Errorf("cleanTitle = %q", got)
	}
}
