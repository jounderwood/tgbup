package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/stats"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func sampleStats() []stats.ChatStats {
	return []stats.ChatStats{
		{
			Item:  stats.Item{ID: -1002263466511, Type: "forum", Title: "Family"},
			Total: 4812, Types: tgclient.TypeCounts{Photos: 310, Videos: 42, Links: 55},
			Topics: []stats.TopicStats{
				{ID: 1, Title: "General", Count: 1612},
				{ID: 3, Title: "Recipes", Count: 300},
				{ID: 5, Title: "Flood", Count: 2900, Excluded: true},
			},
			MissingTopics: []int{99},
		},
		{Item: stats.Item{ID: -20, Type: "group", Title: "Dacha"}, Err: errors.New("FLOOD_WAIT_600")},
		{Item: stats.Item{ID: 123456789, Type: "private", Title: "Mom\nand\tDad"}, Total: 15300, Types: tgclient.TypeCounts{Photos: 1204, Voice: 630}},
	}
}

func TestPrintReport(t *testing.T) {
	var out bytes.Buffer
	printReport(&out, sampleStats())
	got := out.String()
	for _, want := range []string{
		"would archive 3 chats, ~17 212 messages:\n",
		"── 1. Family · forum · -1002263466511 ─",
		"   ~4 812 msgs: photos 310 · videos 42 · links 55\n",
		"   topics 2 of 3:  General #1 ~1 612 · Recipes #3 ~300\n",
		"   excluded:       Flood #5 ~2 900   (~2 900 msgs not saved)\n",
		"   warning: excluded topic #99 is not in the forum\n",
		"── 2. Dacha · group · -20 ─",
		"   ? could not count: FLOOD_WAIT_600\n",
		"── 3. Mom and Dad · private · 123456789 ─",
		"   ~15 300 msgs: photos 1 204 · voice 630\n",
		"══ Total: 3 chats, ~17 212 msgs saved · photos 1 514 · videos 42 · voice 630 · links 55\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "music") || strings.Contains(got, "round") {
		t.Errorf("zero counters printed:\n%s", got)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Mom\nand\tDad":         "Mom and Dad",
		"bell\x07\x1b[31m":      "bell[31m",
		"bad\xff":               "bad",
		strings.Repeat("я", 40): strings.Repeat("я", 29) + "…",
		"  spaced   out ":       "spaced out",
		"семья 👨\u200d👩\u200d👧": "семья 👨\u200d👩\u200d👧",
	}
	for in, want := range cases {
		if got := cleanTitle(in, 30); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProgressWithoutTerminal(t *testing.T) {
	var out bytes.Buffer
	p := progressPrinter(&out, false)
	for i := 1; i <= 25; i++ {
		p(i, 25)
	}
	if got := out.String(); got != "counting 10/25\ncounting 20/25\ncounting 25/25\n" {
		t.Fatalf("progress = %q", got)
	}
}

func TestDryRunExitCode(t *testing.T) {
	ok := []stats.ChatStats{{Total: 1}}
	if code := dryRunExit(ok, nil); code != 0 {
		t.Errorf("clean run = %d", code)
	}
	if code := dryRunExit(ok, []string{"include @x: chat not found"}); code != 1 {
		t.Errorf("warnings = %d", code)
	}
	if code := dryRunExit(sampleStats(), nil); code != 1 {
		t.Errorf("a chat that could not be counted = %d", code)
	}
}
