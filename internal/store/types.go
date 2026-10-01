package store

const (
	ChatPrivate    = "private"
	ChatGroup      = "group"
	ChatSupergroup = "supergroup"
	ChatForum      = "forum"
	ChatChannel    = "channel"

	ChatActive       = "active"
	ChatInaccessible = "inaccessible"

	MediaPending = "pending"
	MediaDone    = "done"
	MediaFailed  = "failed"
	MediaSkipped = "skipped"
)

type Chat struct {
	ID             int64
	Type           string
	Title          string
	Username       string
	Folders        []string
	MigratedFromID int64
	Status         string
}

type Topic struct {
	ChatID    int64
	TopicID   int
	Title     string
	Closed    bool
	CreatedAt int64
}

type User struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
	Partial   bool
}

type Message struct {
	ChatID            int64
	ID                int
	TopicID           int
	Date              int64
	FromID            int64
	Text              string
	EntitiesJSON      string
	FwdFromJSON       string
	ReactionsJSON     string
	ServiceActionJSON string
	// MetaJSON holds non-file media (link preview, location, poll, …); see convert.Meta.
	MetaJSON  string
	ReplyToID int
	GroupedID int64
	Pinned    bool
	EditDate  int64
	Raw       []byte
	TLLayer   int
	MediaKeys []string
}

type Media struct {
	Key      string
	Kind     string
	FileName string
	Mime     string
	Size     int64
}

type Cursor struct {
	NewestID     int
	OldestID     int
	BackfillDone bool
}

type Peer struct {
	ID       int64
	Title    string
	Username string
}

type Batch struct {
	ChatID   int64
	Users    []User
	Peers    []Peer
	Messages []Message
	Media    []Media
	Cursor   Cursor
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
