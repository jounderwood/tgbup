package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// PageKey identifies one rendered page: a chat's (or forum topic's) month.
type PageKey struct {
	ChatID  int64
	TopicID int
	Month   string
}

// MonthKey is a page with a fingerprint of everything shown on it.
type MonthKey struct {
	ChatID      int64
	TopicID     int
	Month       string
	Fingerprint string
}

func (m MonthKey) Page() PageKey { return PageKey{m.ChatID, m.TopicID, m.Month} }

// Months lists every chat/topic month with messages, in chat, topic, month
// order; months follow loc. Empty chatIDs means all chats.
func (s *Store) Months(ctx context.Context, chatIDs []int64, loc *time.Location) ([]MonthKey, error) {
	q := `
SELECT msg.chat_id, COALESCE(msg.topic_id, 0), msg.id, msg.date, COALESCE(msg.edit_date, 0), msg.pinned,
       length(COALESCE(msg.reactions_json, '')), COALESCE(m.status, ''), COALESCE(m.local_path, '')
FROM messages msg
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key`
	args := make([]any, 0, len(chatIDs))
	if len(chatIDs) > 0 {
		q += " WHERE msg.chat_id IN (?" + strings.Repeat(",?", len(chatIDs)-1) + ")"
		for _, id := range chatIDs {
			args = append(args, id)
		}
	}
	q += " ORDER BY msg.chat_id, COALESCE(msg.topic_id, 0), msg.date, msg.id"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		out  []MonthKey
		cur  PageKey
		hash = sha256.New()
	)
	flush := func() {
		if cur.Month != "" {
			out = append(out, MonthKey{cur.ChatID, cur.TopicID, cur.Month, hex.EncodeToString(hash.Sum(nil))[:16]})
		}
		hash.Reset()
	}
	for rows.Next() {
		var (
			k                          PageKey
			id                         int
			date, edit, pinned, reacts int64
			status, path               string
		)
		if err := rows.Scan(&k.ChatID, &k.TopicID, &id, &date, &edit, &pinned, &reacts, &status, &path); err != nil {
			return nil, err
		}
		k.Month = time.Unix(date, 0).In(loc).Format("2006-01")
		if k != cur {
			flush()
			cur = k
		}
		fmt.Fprintf(hash, "%d:%d:%d:%d:%s:%s;", id, edit, pinned, reacts, status, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

func (s *Store) RenderedFingerprints(ctx context.Context) (map[PageKey]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, topic_id, month, fingerprint FROM render_pages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[PageKey]string{}
	for rows.Next() {
		var (
			k  PageKey
			fp string
		)
		if err := rows.Scan(&k.ChatID, &k.TopicID, &k.Month, &fp); err != nil {
			return nil, err
		}
		out[k] = fp
	}
	return out, rows.Err()
}

func (s *Store) SetRendered(ctx context.Context, m MonthKey) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO render_pages (chat_id, topic_id, month, fingerprint) VALUES (?, ?, ?, ?)
ON CONFLICT(chat_id, topic_id, month) DO UPDATE SET fingerprint = excluded.fingerprint`,
		m.ChatID, m.TopicID, m.Month, m.Fingerprint)
	return err
}

type RenderChat struct {
	ID             int64
	Type           string
	Title          string
	Username       string
	MigratedFromID int64
	Topics         map[int]string
}

func (s *Store) RenderChats(ctx context.Context) ([]RenderChat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, type, title, username, COALESCE(migrated_from_id, 0) FROM chats ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []RenderChat
	for rows.Next() {
		c := RenderChat{Topics: map[int]string{}}
		if err := rows.Scan(&c.ID, &c.Type, &c.Title, &c.Username, &c.MigratedFromID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		topics, err := s.Topics(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		for _, t := range topics {
			out[i].Topics[t.TopicID] = t.Title
		}
	}
	return out, nil
}

type RenderMedia struct {
	Kind      string
	FileName  string
	Mime      string
	Size      int64
	Status    string
	LocalPath string
	Error     string
}

type RenderMessage struct {
	Message
	SenderName string
	// FwdName is the name of the forward source when it is a known user or chat.
	FwdName string
	Media   *RenderMedia
}

// senderName resolves from_id against users, then forward-source peers, then archived chats.
const senderName = `COALESCE(NULLIF(trim(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), p.title, c.title, '')`

const senderJoins = `
LEFT JOIN users u ON u.id = msg.from_id
LEFT JOIN peers p ON p.id = msg.from_id
LEFT JOIN chats c ON c.id = msg.from_id`

// MonthMessages returns one page's messages in order, with sender names and media.
func (s *Store) MonthMessages(ctx context.Context, chatID int64, topicID int, month string, loc *time.Location) ([]RenderMessage, error) {
	start, err := time.ParseInLocation("2006-01", month, loc)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT msg.chat_id, msg.id, COALESCE(msg.topic_id, 0), msg.date, COALESCE(msg.from_id, 0), msg.text,
       COALESCE(msg.entities_json, ''), COALESCE(msg.reply_to_id, 0), COALESCE(msg.fwd_from_json, ''),
       COALESCE(msg.reactions_json, ''), COALESCE(msg.service_action_json, ''), msg.pinned, COALESCE(msg.edit_date, 0),
       `+senderName+`,
       COALESCE(NULLIF(trim(COALESCE(fu.first_name, '') || ' ' || COALESCE(fu.last_name, '')), ''), fp.title, fc.title, ''),
       m.kind, m.file_name, m.mime, m.size, m.status, m.local_path, m.error
FROM messages msg`+senderJoins+`
LEFT JOIN users fu ON fu.id = json_extract(msg.fwd_from_json, '$.from_id')
LEFT JOIN peers fp ON fp.id = json_extract(msg.fwd_from_json, '$.from_id')
LEFT JOIN chats fc ON fc.id = json_extract(msg.fwd_from_json, '$.from_id')
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key
WHERE msg.chat_id = ? AND COALESCE(msg.topic_id, 0) = ? AND msg.date >= ? AND msg.date < ?
ORDER BY msg.date, msg.id`, chatID, topicID, start.Unix(), start.AddDate(0, 1, 0).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenderMessage
	for rows.Next() {
		var (
			m                                   RenderMessage
			kind, name, mime, status, path, mer sql.NullString
			size                                sql.NullInt64
		)
		if err := rows.Scan(&m.ChatID, &m.ID, &m.TopicID, &m.Date, &m.FromID, &m.Text, &m.EntitiesJSON, &m.ReplyToID,
			&m.FwdFromJSON, &m.ReactionsJSON, &m.ServiceActionJSON, &m.Pinned, &m.EditDate, &m.SenderName, &m.FwdName,
			&kind, &name, &mime, &size, &status, &path, &mer); err != nil {
			return nil, err
		}
		if kind.Valid {
			m.Media = &RenderMedia{Kind: kind.String, FileName: name.String, Mime: mime.String, Size: size.Int64,
				Status: status.String, LocalPath: path.String, Error: mer.String}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessageRef is what a reply needs to quote and link its target.
type MessageRef struct {
	ChatID     int64
	ID         int
	TopicID    int
	Month      string
	SenderName string
	Text       string
	MediaKind  string
}

func (s *Store) MessageRefs(ctx context.Context, chatID int64, ids []int, loc *time.Location) (map[int]MessageRef, error) {
	out := map[int]MessageRef{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{chatID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT msg.id, COALESCE(msg.topic_id, 0), msg.date, msg.text, `+senderName+`, COALESCE(m.kind, '')
FROM messages msg`+senderJoins+`
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key
WHERE msg.chat_id = ? AND msg.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r := MessageRef{ChatID: chatID}
		var date int64
		if err := rows.Scan(&r.ID, &r.TopicID, &date, &r.Text, &r.SenderName, &r.MediaKind); err != nil {
			return nil, err
		}
		r.Month = time.Unix(date, 0).In(loc).Format("2006-01")
		out[r.ID] = r
	}
	return out, rows.Err()
}

// SearchText is what the search index holds for one message: its text and,
// for media, the file name.
type SearchText struct {
	ID      int
	TopicID int
	Date    int64
	Text    string
}

func (s *Store) SearchTexts(ctx context.Context, chatID int64) ([]SearchText, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT msg.id, COALESCE(msg.topic_id, 0), msg.date, trim(msg.text || ' ' || COALESCE(m.file_name, ''))
FROM messages msg
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key
WHERE msg.chat_id = ? AND (msg.text != '' OR COALESCE(m.file_name, '') != '')
ORDER BY msg.date, msg.id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchText
	for rows.Next() {
		var t SearchText
		if err := rows.Scan(&t.ID, &t.TopicID, &t.Date, &t.Text); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
