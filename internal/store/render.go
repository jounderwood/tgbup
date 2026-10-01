package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// PageKey identifies one rendered page: the n-th page (from 1, oldest first)
// of a chat's or forum topic's message thread.
type PageKey struct {
	ChatID  int64
	TopicID int
	N       int
}

// ThreadPage is a page of a thread with a fingerprint of what it shows.
type ThreadPage struct {
	PageKey
	Total       int   // pages in the thread
	IDs         []int   // message ids, oldest first
	Dates       []int64 // their dates, for day anchors and the calendar
	First, Last int64   // dates of its first and last message
	Fingerprint string
}

// PageQuery narrows paging, for exports. From/To are unix times, To exclusive; 0 is open.
type PageQuery struct {
	ChatIDs  []int64
	Topics   []int
	From, To int64
	Size     int
}

// Pages cuts every chat/topic thread into pages of q.Size messages counted
// from the oldest, so new messages only ever change the last page.
func (s *Store) Pages(ctx context.Context, q PageQuery) ([]ThreadPage, error) {
	query := `
SELECT msg.chat_id, COALESCE(msg.topic_id, 0), msg.id, msg.date, COALESCE(msg.edit_date, 0), msg.pinned,
       COALESCE(msg.reactions_json, '') || COALESCE(msg.meta_json, ''), COALESCE(m.status, ''), COALESCE(m.local_path, '')
FROM messages msg
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key
WHERE 1 = 1`
	var args []any
	if len(q.ChatIDs) > 0 {
		query += " AND msg.chat_id IN (?" + strings.Repeat(",?", len(q.ChatIDs)-1) + ")"
		for _, id := range q.ChatIDs {
			args = append(args, id)
		}
	}
	if len(q.Topics) > 0 {
		query += " AND COALESCE(msg.topic_id, 0) IN (?" + strings.Repeat(",?", len(q.Topics)-1) + ")"
		for _, id := range q.Topics {
			args = append(args, id)
		}
	}
	if q.From != 0 {
		query += " AND msg.date >= ?"
		args = append(args, q.From)
	}
	if q.To != 0 {
		query += " AND msg.date < ?"
		args = append(args, q.To)
	}
	query += " ORDER BY msg.chat_id, COALESCE(msg.topic_id, 0), msg.date, msg.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		out  []ThreadPage
		cur  *ThreadPage
		hash = sha256.New()
		size = max(q.Size, 1)
	)
	flush := func() {
		if cur != nil {
			cur.Fingerprint = hex.EncodeToString(hash.Sum(nil))[:16]
			out = append(out, *cur)
		}
		hash.Reset()
		cur = nil
	}
	for rows.Next() {
		var (
			chat                int64
			topic, id           int
			date, edit, pinned  int64
			extra, status, path string
		)
		if err := rows.Scan(&chat, &topic, &id, &date, &edit, &pinned, &extra, &status, &path); err != nil {
			return nil, err
		}
		next := 1
		switch {
		case cur != nil && (cur.ChatID != chat || cur.TopicID != topic):
			flush()
		case cur != nil && len(cur.IDs) == size:
			next = cur.N + 1
			flush()
		}
		if cur == nil {
			cur = &ThreadPage{PageKey: PageKey{chat, topic, next}, First: date}
		}
		cur.IDs = append(cur.IDs, id)
		cur.Dates = append(cur.Dates, date)
		cur.Last = date
		fmt.Fprintf(hash, "%d:%d:%d:%s:%s:%s;", id, edit, pinned, extra, status, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	// Every page shows the thread's page count in its pager.
	for i := 0; i < len(out); {
		j := i
		for j < len(out) && out[j].ChatID == out[i].ChatID && out[j].TopicID == out[i].TopicID {
			j++
		}
		for k := i; k < j; k++ {
			out[k].Total = j - i
		}
		i = j
	}
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
			k        PageKey
			page, fp string
		)
		if err := rows.Scan(&k.ChatID, &k.TopicID, &page, &fp); err != nil {
			return nil, err
		}
		// Rows from the old one-page-per-month layout have no number and never match.
		if k.N, err = strconv.Atoi(page); err == nil {
			out[k] = fp
		}
	}
	return out, rows.Err()
}

// SetRendered records the fingerprint a page was drawn with. The month column
// predates paging and now holds the page number.
func (s *Store) SetRendered(ctx context.Context, k PageKey, fingerprint string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO render_pages (chat_id, topic_id, month, fingerprint) VALUES (?, ?, ?, ?)
ON CONFLICT(chat_id, topic_id, month) DO UPDATE SET fingerprint = excluded.fingerprint`,
		k.ChatID, k.TopicID, strconv.Itoa(k.N), fingerprint)
	return err
}

// ClearRendered forgets all fingerprints, so the next render draws every page.
func (s *Store) ClearRendered(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM render_pages`)
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
	Key       string
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

// PageMessages returns a page's messages in order, with sender names and media.
func (s *Store) PageMessages(ctx context.Context, chatID int64, ids []int) ([]RenderMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{chatID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT msg.chat_id, msg.id, COALESCE(msg.topic_id, 0), msg.date, COALESCE(msg.from_id, 0), msg.text,
       COALESCE(msg.entities_json, ''), COALESCE(msg.reply_to_id, 0), COALESCE(msg.fwd_from_json, ''),
       COALESCE(msg.reactions_json, ''), COALESCE(msg.service_action_json, ''), msg.pinned, COALESCE(msg.edit_date, 0),
       COALESCE(msg.meta_json, ''), `+senderName+`,
       COALESCE(NULLIF(trim(COALESCE(fu.first_name, '') || ' ' || COALESCE(fu.last_name, '')), ''), fp.title, fc.title, ''),
       m.key, m.kind, m.file_name, m.mime, m.size, m.status, m.local_path, m.error
FROM messages msg`+senderJoins+`
LEFT JOIN users fu ON fu.id = json_extract(msg.fwd_from_json, '$.from_id')
LEFT JOIN peers fp ON fp.id = json_extract(msg.fwd_from_json, '$.from_id')
LEFT JOIN chats fc ON fc.id = json_extract(msg.fwd_from_json, '$.from_id')
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key
WHERE msg.chat_id = ? AND msg.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)
ORDER BY msg.date, msg.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenderMessage
	for rows.Next() {
		var (
			m                                        RenderMessage
			key, kind, name, mime, status, path, mer sql.NullString
			size                                     sql.NullInt64
		)
		if err := rows.Scan(&m.ChatID, &m.ID, &m.TopicID, &m.Date, &m.FromID, &m.Text, &m.EntitiesJSON, &m.ReplyToID,
			&m.FwdFromJSON, &m.ReactionsJSON, &m.ServiceActionJSON, &m.Pinned, &m.EditDate, &m.MetaJSON, &m.SenderName, &m.FwdName,
			&key, &kind, &name, &mime, &size, &status, &path, &mer); err != nil {
			return nil, err
		}
		if kind.Valid {
			m.Media = &RenderMedia{Key: key.String, Kind: kind.String, FileName: name.String, Mime: mime.String, Size: size.Int64,
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
	SenderName string
	Text       string
	MediaKind  string
}

func (s *Store) MessageRefs(ctx context.Context, chatID int64, ids []int) (map[int]MessageRef, error) {
	out := map[int]MessageRef{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{chatID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT msg.id, COALESCE(msg.topic_id, 0), msg.text, `+senderName+`, COALESCE(m.kind, '')
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
		if err := rows.Scan(&r.ID, &r.TopicID, &r.Text, &r.SenderName, &r.MediaKind); err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// SearchText is what the search index holds for one message: its text and,
// for media, the file name.
type SearchText struct {
	ID       int
	TopicID  int
	Date     int64
	Text     string
	MetaJSON string
}

func (s *Store) SearchTexts(ctx context.Context, chatID int64) ([]SearchText, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT msg.id, COALESCE(msg.topic_id, 0), msg.date, trim(msg.text || ' ' || COALESCE(m.file_name, '')), COALESCE(msg.meta_json, '')
FROM messages msg
LEFT JOIN message_media mm ON mm.chat_id = msg.chat_id AND mm.message_id = msg.id
LEFT JOIN media m ON m.key = mm.media_key
WHERE msg.chat_id = ? AND (msg.text != '' OR COALESCE(m.file_name, '') != '' OR msg.meta_json IS NOT NULL)
ORDER BY msg.date, msg.id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchText
	for rows.Next() {
		var t SearchText
		if err := rows.Scan(&t.ID, &t.TopicID, &t.Date, &t.Text, &t.MetaJSON); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Names resolves user and chat ids to display names; unknown ids are left out.
func (s *Store) Names(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		var name string
		err := s.db.QueryRowContext(ctx, `
SELECT `+senderName+`
FROM (SELECT ? AS from_id) msg`+senderJoins, id).Scan(&name)
		if err != nil {
			return nil, err
		}
		if name != "" {
			out[id] = name
		}
	}
	return out, nil
}
