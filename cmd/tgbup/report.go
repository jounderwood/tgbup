package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/term"

	"gitlab.com/joeunderwood/tgbup/internal/stats"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

const (
	titleWidth  = 30
	headerWidth = 66
	lineWidth   = 100
)

func printReport(w io.Writer, chats []stats.ChatStats) {
	saved := 0
	var types tgclient.TypeCounts
	for _, c := range chats {
		if c.Err == nil {
			saved += c.Saved()
			types = addTypes(types, c.Types)
		}
	}
	fmt.Fprintf(w, "would archive %d chats, ~%s messages:\n", len(chats), groupDigits(saved))
	for i, c := range chats {
		header := fmt.Sprintf("── %d. %s · %s · %d ", i+1, cleanTitle(c.Item.Title, titleWidth), c.Item.Type, c.Item.ID)
		fmt.Fprintln(w, header+strings.Repeat("─", max(headerWidth-displayWidth(header), 2)))
		if c.Err != nil {
			fmt.Fprintf(w, "   ? could not count: %v\n", c.Err)
			continue
		}
		line := "~" + groupDigits(c.Total) + " msgs"
		if t := typeList(c.Types); t != "" {
			line += ": " + t
		}
		fmt.Fprintln(w, "   "+line)
		printTopics(w, c)
		for _, id := range c.MissingTopics {
			fmt.Fprintf(w, "   warning: excluded topic #%d is not in the forum\n", id)
		}
	}
	total := fmt.Sprintf("══ Total: %d chats, ~%s msgs saved", len(chats), groupDigits(saved))
	if t := typeList(types); t != "" {
		total += " · " + t
	}
	fmt.Fprintln(w, total)
}

func printTopics(w io.Writer, c stats.ChatStats) {
	if len(c.Topics) == 0 {
		return
	}
	var kept, dropped []string
	notSaved := 0
	for _, t := range c.Topics {
		s := fmt.Sprintf("%s #%d ~%s", cleanTitle(t.Title, titleWidth), t.ID, groupDigits(t.Count))
		if t.Excluded {
			dropped = append(dropped, s)
			notSaved += t.Count
		} else {
			kept = append(kept, s)
		}
	}
	// The label column fits the longer label plus two spaces, so a long count never touches the list.
	topicsLabel := fmt.Sprintf("topics %d of %d:", len(kept), len(c.Topics))
	col := max(len(topicsLabel), len("excluded:")) + 2
	indent := strings.Repeat(" ", 3+col)
	if len(kept) > 0 {
		fmt.Fprint(w, wrapList("   "+padRight(topicsLabel, col), indent, kept))
	}
	if len(dropped) > 0 {
		list := strings.TrimSuffix(wrapList("   "+padRight("excluded:", col), indent, dropped), "\n")
		fmt.Fprintf(w, "%s   (~%s msgs not saved)\n", list, groupDigits(notSaved))
	}
}

// wrapList joins items with " · ", breaking lines at lineWidth.
func wrapList(first, indent string, items []string) string {
	var b strings.Builder
	line := first
	for i, it := range items {
		sep := ""
		if i > 0 {
			sep = " · "
		}
		if i > 0 && displayWidth(line+sep+it) > lineWidth {
			b.WriteString(line + " ·\n")
			line, sep = indent, ""
		}
		line += sep + it
	}
	b.WriteString(line + "\n")
	return b.String()
}

func typeList(t tgclient.TypeCounts) string {
	var parts []string
	for _, p := range []struct {
		name string
		n    int
	}{
		{"photos", t.Photos}, {"videos", t.Videos}, {"files", t.Files}, {"voice", t.Voice},
		{"round", t.Round}, {"music", t.Music}, {"gifs", t.GIFs}, {"links", t.Links},
	} {
		if p.n > 0 {
			parts = append(parts, p.name+" "+groupDigits(p.n))
		}
	}
	return strings.Join(parts, " · ")
}

func addTypes(a, b tgclient.TypeCounts) tgclient.TypeCounts {
	return tgclient.TypeCounts{
		Photos: a.Photos + b.Photos, Videos: a.Videos + b.Videos, Files: a.Files + b.Files, Voice: a.Voice + b.Voice,
		Round: a.Round + b.Round, Music: a.Music + b.Music, GIFs: a.GIFs + b.GIFs, Links: a.Links + b.Links,
	}
}

// cleanTitle makes a chat title safe for a one-line table cell: titles come
// from other people and may carry newlines, tabs or terminal escape codes.
func cleanTitle(s string, width int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsGraphic(r) || r == '‍':
			return r
		}
		return -1
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	return truncateWidth(s, width)
}

func groupDigits(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// progressPrinter reports counting progress: one rewritten line on a
// terminal, every tenth chat in logs.
func progressPrinter(w io.Writer, tty bool) func(done, total int) {
	return func(done, total int) {
		switch {
		case tty && done == total:
			fmt.Fprint(w, "\r\033[K")
		case tty:
			fmt.Fprintf(w, "\rcounting %d/%d…", done, total)
		case done%10 == 0 || done == total:
			fmt.Fprintf(w, "counting %d/%d\n", done, total)
		}
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func statItems(targets []tgclient.Target) []stats.Item {
	items := make([]stats.Item, 0, len(targets))
	for _, t := range targets {
		items = append(items, stats.Item{ID: t.Chat.ID, Type: t.Chat.Type, Title: t.Chat.Title, Peer: t.Peer, ExcludedTopics: t.ExcludedTopics})
	}
	return items
}
