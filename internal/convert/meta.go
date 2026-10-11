package convert

import (
	"encoding/json"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// Meta is what a message carries besides text and files, kept as Telegram
// returned it at archive time. One JSON object, keyed by kind, so new kinds
// need no schema change.
type Meta struct {
	WebPage *WebPageMeta `json:"webpage,omitempty"`
	Geo     *GeoMeta     `json:"geo,omitempty"`
	Venue   *VenueMeta   `json:"venue,omitempty"`
	Live    *LiveMeta    `json:"live,omitempty"`
	Contact *ContactMeta `json:"contact,omitempty"`
	Poll    *PollMeta    `json:"poll,omitempty"`
	Todo    *TodoMeta    `json:"todo,omitempty"`
	Dice    *DiceMeta    `json:"dice,omitempty"`
	Sticker *StickerMeta `json:"sticker,omitempty"`
	Other   *OtherMeta   `json:"other,omitempty"`
}

type WebPageMeta struct {
	URL         string `json:"url,omitempty"`
	DisplayURL  string `json:"display_url,omitempty"`
	Type        string `json:"type,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	PhotoKey    string `json:"photo_key,omitempty"`
}

type GeoMeta struct {
	Lat      float64 `json:"lat"`
	Long     float64 `json:"long"`
	Accuracy int     `json:"accuracy,omitempty"`
}

type VenueMeta struct {
	Title    string `json:"title,omitempty"`
	Address  string `json:"address,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type LiveMeta struct {
	Period  int `json:"period,omitempty"`
	Heading int `json:"heading,omitempty"`
}

type ContactMeta struct {
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Phone     string `json:"phone,omitempty"`
	UserID    int64  `json:"user_id,omitempty"`
}

type PollMeta struct {
	Question    string       `json:"question"`
	Answers     []PollAnswer `json:"answers"`
	TotalVoters int          `json:"total_voters,omitempty"`
	Closed      bool         `json:"closed,omitempty"`
	Quiz        bool         `json:"quiz,omitempty"`
	Multiple    bool         `json:"multiple,omitempty"`
}

type PollAnswer struct {
	Text    string `json:"text"`
	Voters  int    `json:"voters,omitempty"`
	Correct bool   `json:"correct,omitempty"`
}

type TodoMeta struct {
	Title             string     `json:"title"`
	Items             []TodoItem `json:"items"`
	OthersCanAppend   bool       `json:"others_can_append,omitempty"`
	OthersCanComplete bool       `json:"others_can_complete,omitempty"`
}

type TodoItem struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	DoneBy int64  `json:"done_by,omitempty"`
	DoneAt int    `json:"done_at,omitempty"`
}

type DiceMeta struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}

// StickerMeta: a sticker is kept as its emoji, not as a file.
type StickerMeta struct {
	Emoji string `json:"emoji,omitempty"`
}

type OtherMeta struct {
	Type string `json:"type"`
}

func geo(g tg.GeoPointClass) *GeoMeta {
	p, ok := g.(*tg.GeoPoint)
	if !ok {
		return nil
	}
	acc, _ := p.GetAccuracyRadius()
	return &GeoMeta{Lat: p.Lat, Long: p.Long, Accuracy: acc}
}

// metaOf describes the message's non-file media; photos and documents are
// files and have no meta, except a sticker: its emoji. ok is false when
// there is nothing to keep.
func metaOf(m *tg.Message) (Meta, bool) {
	mc, ok := m.GetMedia()
	if !ok {
		return Meta{}, false
	}
	var out Meta
	switch v := mc.(type) {
	case *tg.MessageMediaPhoto, *tg.MessageMediaEmpty:
		return Meta{}, false
	case *tg.MessageMediaDocument:
		s, ok := stickerOf(v)
		if !ok {
			return Meta{}, false
		}
		out.Sticker = &StickerMeta{Emoji: s.Alt}
	case *tg.MessageMediaWebPage:
		out.WebPage = webPage(v.Webpage)
	case *tg.MessageMediaGeo:
		out.Geo = geo(v.Geo)
	case *tg.MessageMediaVenue:
		out.Geo = geo(v.Geo)
		out.Venue = &VenueMeta{Title: v.Title, Address: v.Address, Provider: v.Provider}
	case *tg.MessageMediaGeoLive:
		out.Geo = geo(v.Geo)
		heading, _ := v.GetHeading()
		out.Live = &LiveMeta{Period: v.Period, Heading: heading}
	case *tg.MessageMediaContact:
		out.Contact = &ContactMeta{FirstName: v.FirstName, LastName: v.LastName, Phone: v.PhoneNumber, UserID: v.UserID}
	case *tg.MessageMediaPoll:
		out.Poll = poll(v)
	case *tg.MessageMediaToDo:
		out.Todo = todo(v)
	case *tg.MessageMediaDice:
		out.Dice = &DiceMeta{Emoji: v.Emoticon, Value: v.Value}
	default:
		out.Other = &OtherMeta{Type: mc.TypeName()}
	}
	return out, true
}

func webPage(wc tg.WebPageClass) *WebPageMeta {
	switch w := wc.(type) {
	case *tg.WebPage:
		out := &WebPageMeta{URL: w.URL, DisplayURL: w.DisplayURL, Type: w.Type, SiteName: w.SiteName, Title: w.Title, Description: w.Description}
		if p, ok := webPagePhoto(w); ok {
			out.PhotoKey = photoKey(p)
		}
		return out
	case *tg.WebPagePending:
		return &WebPageMeta{URL: w.URL}
	case *tg.WebPageEmpty:
		return &WebPageMeta{URL: w.URL}
	}
	return &WebPageMeta{}
}

func webPagePhoto(w *tg.WebPage) (*tg.Photo, bool) {
	pc, ok := w.GetPhoto()
	if !ok {
		return nil, false
	}
	p, ok := pc.(*tg.Photo)
	return p, ok
}

func poll(v *tg.MessageMediaPoll) *PollMeta {
	out := &PollMeta{Question: v.Poll.Question.Text, TotalVoters: v.Results.TotalVoters,
		Closed: v.Poll.Closed, Quiz: v.Poll.Quiz, Multiple: v.Poll.MultipleChoice}
	for _, ac := range v.Poll.Answers {
		a, ok := ac.(*tg.PollAnswer)
		if !ok {
			continue
		}
		pa := PollAnswer{Text: a.Text.Text}
		for _, r := range v.Results.Results {
			if string(r.Option) == string(a.Option) {
				pa.Voters, pa.Correct = r.Voters, r.Correct
			}
		}
		out.Answers = append(out.Answers, pa)
	}
	return out
}

func todo(v *tg.MessageMediaToDo) *TodoMeta {
	out := &TodoMeta{Title: v.Todo.Title.Text, OthersCanAppend: v.Todo.OthersCanAppend, OthersCanComplete: v.Todo.OthersCanComplete}
	for _, it := range v.Todo.List {
		item := TodoItem{ID: it.ID, Title: it.Title.Text}
		for _, c := range v.Completions {
			if c.ID == it.ID {
				item.DoneBy, item.DoneAt = PeerID(c.CompletedBy), c.Date
			}
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// MetaJSON is the meta of a message kept as Telegram gave it (raw).
func MetaJSON(raw []byte) (string, error) {
	mc, err := tg.DecodeMessage(&bin.Buffer{Buf: raw})
	if err != nil {
		return "", err
	}
	m, ok := mc.(*tg.Message)
	if !ok {
		return "", nil
	}
	return metaJSON(m)
}

func metaJSON(m *tg.Message) (string, error) {
	meta, ok := metaOf(m)
	if !ok {
		return "", nil
	}
	b, err := json.Marshal(meta)
	return string(b), err
}
