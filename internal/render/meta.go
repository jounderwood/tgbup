package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jounderwood/tgbup/internal/convert"
)

// PageMeta is the message's non-file media prepared for display.
type PageMeta struct {
	Link    *LinkCard
	Place   *PlaceCard
	Contact *ContactCard
	Poll    *PollCard
	Todo    *TodoCard
	Dice    string
	Other   string
}

type LinkCard struct {
	Href        template.URL // "" when the URL is not safe to link
	URL         string
	Site        string
	Title       string
	Description string
	Photo       string // picture href, "" when not downloaded
}

type PlaceCard struct {
	Name   string
	Coords string
	MapURL string
	Live   bool
}

type ContactCard struct {
	Name  string
	Phone string
	Tel   template.URL
}

type PollCard struct {
	Question string
	Answers  []PollRow
	Total    int
	Flags    string
}

type PollRow struct {
	Text    string
	Voters  int
	Percent int
	Correct bool
}

type TodoCard struct {
	Title string
	Items []TodoRow
}

type TodoRow struct {
	Title string
	Done  bool
	By    string
}

func parseMeta(metaJSON string) (convert.Meta, bool) {
	var m convert.Meta
	if metaJSON == "" || json.Unmarshal([]byte(metaJSON), &m) != nil {
		return m, false
	}
	return m, true
}

var phoneRE = regexp.MustCompile(`^\+?[0-9 ()-]{3,}$`)

// buildMeta prepares cards; photoHref is the downloaded link-preview picture, if any.
func buildMeta(m convert.Meta, photoHref string, names map[int64]string, loc *time.Location) *PageMeta {
	out := &PageMeta{}
	if w := m.WebPage; w != nil && (w.Title != "" || w.Description != "" || w.SiteName != "" || photoHref != "") {
		card := &LinkCard{URL: w.URL, Site: w.SiteName, Title: orDefault(w.Title, w.DisplayURL), Description: w.Description, Photo: photoHref}
		if href, ok := safeURL(w.URL); ok {
			card.Href = template.URL(href) //nolint:gosec // scheme checked by safeURL
		}
		out.Link = card
	}
	if g := m.Geo; g != nil {
		lat, long := strconv.FormatFloat(g.Lat, 'f', -1, 64), strconv.FormatFloat(g.Long, 'f', -1, 64)
		p := &PlaceCard{Coords: lat + ", " + long, Live: m.Live != nil,
			MapURL: "https://www.google.com/maps/search/?api=1&query=" + lat + "," + long}
		if v := m.Venue; v != nil {
			p.Name = strings.Trim(v.Title+", "+v.Address, ", ")
		}
		out.Place = p
	}
	if c := m.Contact; c != nil {
		card := &ContactCard{Name: strings.TrimSpace(c.FirstName + " " + c.LastName), Phone: c.Phone}
		if phoneRE.MatchString(c.Phone) {
			card.Tel = template.URL("tel:" + strings.ReplaceAll(c.Phone, " ", "")) //nolint:gosec // digits only
		}
		out.Contact = card
	}
	if p := m.Poll; p != nil {
		card := &PollCard{Question: p.Question, Total: p.TotalVoters}
		var flags []string
		if p.Quiz {
			flags = append(flags, "quiz")
		}
		if p.Multiple {
			flags = append(flags, "multiple answers")
		}
		if p.Closed {
			flags = append(flags, "closed")
		}
		card.Flags = strings.Join(flags, " · ")
		for _, a := range p.Answers {
			row := PollRow{Text: a.Text, Voters: a.Voters, Correct: a.Correct}
			if p.TotalVoters > 0 {
				row.Percent = (a.Voters*100 + p.TotalVoters/2) / p.TotalVoters
			}
			card.Answers = append(card.Answers, row)
		}
		out.Poll = card
	}
	if t := m.Todo; t != nil {
		card := &TodoCard{Title: t.Title}
		for _, it := range t.Items {
			row := TodoRow{Title: it.Title, Done: it.DoneAt != 0}
			if row.Done {
				row.By = orDefault(names[it.DoneBy], "someone") + " · " + time.Unix(int64(it.DoneAt), 0).In(loc).Format("2006-01-02")
			}
			card.Items = append(card.Items, row)
		}
		out.Todo = card
	}
	if d := m.Dice; d != nil {
		out.Dice = fmt.Sprintf("%s %d", d.Emoji, d.Value)
	}
	if o := m.Other; o != nil {
		out.Other = o.Type
	}
	return out
}

// metaText is the searchable text of a message's meta.
func metaText(metaJSON string) string {
	m, ok := parseMeta(metaJSON)
	if !ok {
		return ""
	}
	var parts []string
	if w := m.WebPage; w != nil {
		parts = append(parts, w.SiteName, w.Title, w.Description)
	}
	if v := m.Venue; v != nil {
		parts = append(parts, v.Title, v.Address)
	}
	if c := m.Contact; c != nil {
		parts = append(parts, c.FirstName, c.LastName, c.Phone)
	}
	if p := m.Poll; p != nil {
		parts = append(parts, p.Question)
		for _, a := range p.Answers {
			parts = append(parts, a.Text)
		}
	}
	if t := m.Todo; t != nil {
		parts = append(parts, t.Title)
		for _, it := range t.Items {
			parts = append(parts, it.Title)
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// todoUsers lists who completed checklist items, to resolve their names.
func todoUsers(metaJSON string) []int64 {
	m, ok := parseMeta(metaJSON)
	if !ok || m.Todo == nil {
		return nil
	}
	var ids []int64
	for _, it := range m.Todo.Items {
		if it.DoneBy != 0 {
			ids = append(ids, it.DoneBy)
		}
	}
	return ids
}
