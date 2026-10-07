package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type MediaJob struct {
	Key       string
	Kind      string
	FileName  string
	Mime      string
	Size      int64
	Attempts  int
	ChatID    int64
	MessageID int
	// Date, ChatTitle and Sender are the message's the file is downloaded
	// through; they name the file.
	Date      int64
	ChatTitle string
	Sender    string
	// LocalPath is the name chosen by an earlier attempt; "" until one is chosen.
	LocalPath string
}

type MediaInfo struct {
	Status    string
	LocalPath string
	Error     string
	Attempts  int
}

func (s *Store) PendingMedia(ctx context.Context) ([]MediaJob, error) {
	// chat_id and message_id must come from the same link: a file forwarded between
	// chats has several links, and mixing them points at a message that does not exist.
	rows, err := s.db.QueryContext(ctx, `
SELECT m.key, m.kind, m.file_name, m.mime, m.size, m.attempts, mm.chat_id, mm.message_id,
       msg.date, ch.title, `+senderName+`, COALESCE(m.local_path, '')
FROM media m
JOIN message_media mm ON mm.rowid = (SELECT min(rowid) FROM message_media WHERE media_key = m.key)
JOIN messages msg ON msg.chat_id = mm.chat_id AND msg.id = mm.message_id
JOIN chats ch ON ch.id = mm.chat_id`+senderJoins+`
WHERE m.status = 'pending'
ORDER BY m.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaJob
	for rows.Next() {
		var j MediaJob
		if err := rows.Scan(&j.Key, &j.Kind, &j.FileName, &j.Mime, &j.Size, &j.Attempts, &j.ChatID, &j.MessageID,
			&j.Date, &j.ChatTitle, &j.Sender, &j.LocalPath); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ReserveMediaPath gives the file key the name path unless another file has
// it (compared by pathKey); false means the name is taken.
func (s *Store) ReserveMediaPath(ctx context.Context, key, path, pathKey string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE media SET local_path = ?, path_key = ?
WHERE key = ? AND NOT EXISTS (SELECT 1 FROM media WHERE path_key = ?)`, path, pathKey, key, pathKey)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) MediaDone(ctx context.Context, key, localPath string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'done', local_path = ?, error = NULL WHERE key = ?`, localPath, key)
	return err
}

func (s *Store) MediaSkipped(ctx context.Context, key, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'skipped', error = ? WHERE key = ?`, reason, key)
	return err
}

func (s *Store) SkippedMedia(ctx context.Context) ([]Media, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, kind, size FROM media WHERE status = 'skipped'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		var m Media
		if err := rows.Scan(&m.Key, &m.Kind, &m.Size); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) RequeueMedia(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'pending', error = NULL WHERE key = ?`, key)
	return err
}

// MediaAttemptFailed counts a failed download; gaveUp is true once the file
// has used all its attempts and is marked failed.
func (s *Store) MediaAttemptFailed(ctx context.Context, key, errMsg string, maxAttempts int) (gaveUp bool, err error) {
	var status string
	err = s.db.QueryRowContext(ctx, `
UPDATE media SET attempts = attempts + 1, error = ?,
  status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END
WHERE key = ? RETURNING status`, errMsg, maxAttempts, key).Scan(&status)
	return status == MediaFailed, err
}

func (s *Store) MediaInfo(ctx context.Context, key string) (MediaInfo, error) {
	var (
		i         MediaInfo
		path, msg sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT status, local_path, error, attempts FROM media WHERE key = ?`, key).
		Scan(&i.Status, &path, &msg, &i.Attempts)
	i.LocalPath, i.Error = path.String, msg.String
	return i, err
}

func (s *Store) MessageRaw(ctx context.Context, chatID int64, id int) ([]byte, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT raw FROM messages WHERE chat_id = ? AND id = ?`, chatID, id).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("message %d/%d: %w", chatID, id, err)
	}
	return raw, nil
}

func (s *Store) UpdateMessageRaw(ctx context.Context, chatID int64, id int, raw []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET raw = ? WHERE chat_id = ? AND id = ?`, raw, chatID, id)
	return err
}

// CountMedia counts files in a status (e.g. "failed": given up after all attempts).
func (s *Store) CountMedia(ctx context.Context, status string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM media WHERE status = ?`, status).Scan(&n)
	return n, err
}

// ReleaseMediaPath forgets the name reserved for key, so another can be chosen.
func (s *Store) ReleaseMediaPath(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET local_path = NULL, path_key = NULL WHERE key = ?`, key)
	return err
}

// TodoEvent is a checklist event: who ticked or added items in which checklist.
type TodoEvent struct {
	ChatID     int64
	Checklist  int // the checklist message the event refers to
	FromID     int64
	Date       int64
	ActionJSON string
}

// TodoEvents lists checklist events in the order they happened.
func (s *Store) TodoEvents(ctx context.Context) ([]TodoEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT chat_id, reply_to_id, COALESCE(from_id, 0), date, service_action_json FROM messages
WHERE reply_to_id IS NOT NULL AND service_action_json LIKE '{"type":"messageActionTodo%'
ORDER BY date, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoEvent
	for rows.Next() {
		var e TodoEvent
		if err := rows.Scan(&e.ChatID, &e.Checklist, &e.FromID, &e.Date, &e.ActionJSON); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MessageMeta returns a message's meta_json; "" when there is none or no such message.
func (s *Store) MessageMeta(ctx context.Context, chatID int64, id int) (string, error) {
	var meta sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT meta_json FROM messages WHERE chat_id = ? AND id = ?`, chatID, id).Scan(&meta)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return meta.String, err
}

func (s *Store) SetMessageMeta(ctx context.Context, chatID int64, id int, meta string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET meta_json = ? WHERE chat_id = ? AND id = ?`, meta, chatID, id)
	return err
}
