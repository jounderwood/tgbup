package store

import (
	"context"
	"database/sql"
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
SELECT m.key, m.kind, m.file_name, m.mime, m.size, m.attempts, mm.chat_id, mm.message_id
FROM media m
JOIN message_media mm ON mm.rowid = (SELECT min(rowid) FROM message_media WHERE media_key = m.key)
WHERE m.status = 'pending'
ORDER BY m.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaJob
	for rows.Next() {
		var j MediaJob
		if err := rows.Scan(&j.Key, &j.Kind, &j.FileName, &j.Mime, &j.Size, &j.Attempts, &j.ChatID, &j.MessageID); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
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

func (s *Store) MediaAttemptFailed(ctx context.Context, key, errMsg string, maxAttempts int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE media SET attempts = attempts + 1, error = ?,
  status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END
WHERE key = ?`, errMsg, maxAttempts, key)
	return err
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
