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
	ID         int // the event's own message id
	Checklist  int // the checklist message the event refers to
	FromID     int64
	Date       int64
	ActionJSON string
	// ReadUpto is the checklist's read mark: the chat's newest id when it
	// was last read from Telegram, whose state has every event up to it; 0:
	// not read since tgbup kept the mark.
	ReadUpto int
}

// TodoEvents lists checklist events in the order they happened.
func (s *Store) TodoEvents(ctx context.Context) ([]TodoEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.chat_id, e.id, e.reply_to_id, COALESCE(e.from_id, 0), e.date, e.service_action_json, COALESCE(c.read_upto_id, 0)
FROM messages e
LEFT JOIN messages c ON c.chat_id = e.chat_id AND c.id = e.reply_to_id
WHERE e.reply_to_id IS NOT NULL AND e.service_action_json LIKE '{"type":"messageActionTodo%'
ORDER BY e.date, e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoEvent
	for rows.Next() {
		var e TodoEvent
		if err := rows.Scan(&e.ChatID, &e.ID, &e.Checklist, &e.FromID, &e.Date, &e.ActionJSON, &e.ReadUpto); err != nil {
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

// DoneMediaPath is a downloaded file and where it is, relative to the archive.
type DoneMediaPath struct{ Key, Path string }

// DoneMediaPaths lists every downloaded file.
func (s *Store) DoneMediaPaths(ctx context.Context) ([]DoneMediaPath, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, local_path FROM media WHERE status = 'done' AND local_path IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DoneMediaPath
	for rows.Next() {
		var m DoneMediaPath
		if err := rows.Scan(&m.Key, &m.Path); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MoveMediaPath records that a downloaded file was renamed.
func (s *Store) MoveMediaPath(ctx context.Context, key, path, pathKey string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET local_path = ?, path_key = ? WHERE key = ?`, path, pathKey, key)
	return err
}

// StickerFile is a sticker kept as a file, from before stickers were kept as
// their emoji. Path is the name reserved for it, relative to the archive
// (downloaded or not: a download stopped half-way has a file there too); ""
// if none was.
type StickerFile struct {
	Key, Path string
	Size      int64
}

// StickerFiles lists the stickers kept as files.
func (s *Store) StickerFiles(ctx context.Context) ([]StickerFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, COALESCE(local_path, ''), size FROM media WHERE kind = 'sticker'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StickerFile
	for rows.Next() {
		var f StickerFile
		if err := rows.Scan(&f.Key, &f.Path, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ForgetStickers makes the stickers kept as files their emoji: each of their
// messages gets the sticker meta that meta finds in the message as Telegram
// gave it, and the files' records go. The files on disk are the caller's.
func (s *Store) ForgetStickers(ctx context.Context, meta func(raw []byte) (string, bool)) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	type msg struct {
		chat int64
		id   int
		raw  []byte
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.chat_id, m.id, m.raw FROM messages m
JOIN message_media mm ON mm.chat_id = m.chat_id AND mm.message_id = m.id
JOIN media md ON md.key = mm.media_key WHERE md.kind = 'sticker'`)
	if err != nil {
		return 0, err
	}
	var msgs []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.chat, &m.id, &m.raw); err != nil {
			rows.Close()
			return 0, err
		}
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, m := range msgs {
		q, args := `UPDATE messages SET meta_json = ? WHERE chat_id = ? AND id = ?`, []any{"", m.chat, m.id}
		if mj, ok := meta(m.raw); ok {
			args[0] = mj
		} else {
			// A sticker all the same, its emoji unknown; meta it has is kept.
			q, args[0] = q+` AND meta_json IS NULL`, `{"sticker":{}}`
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_media WHERE media_key IN (SELECT key FROM media WHERE kind = 'sticker')`); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM media WHERE kind = 'sticker'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}
