package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Store) SaveBatch(ctx context.Context, b Batch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()

	for _, u := range b.Users {
		q := `INSERT INTO users (id, first_name, last_name, username, is_bot, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET first_name = excluded.first_name, last_name = excluded.last_name,
  username = excluded.username, is_bot = excluded.is_bot, updated_at = excluded.updated_at`
		if u.Partial {
			q = `INSERT INTO users (id, first_name, last_name, username, is_bot, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`
		}
		if _, err := tx.ExecContext(ctx, q, u.ID, u.FirstName, u.LastName, u.Username, u.IsBot, now); err != nil {
			return fmt.Errorf("save user %d: %w", u.ID, err)
		}
	}
	for _, p := range b.Peers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO peers (id, title, username, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET title = excluded.title, username = excluded.username, updated_at = excluded.updated_at`,
			p.ID, p.Title, p.Username, now); err != nil {
			return fmt.Errorf("save peer %d: %w", p.ID, err)
		}
	}
	for _, m := range b.Media {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO media (key, kind, file_name, mime, size) VALUES (?, ?, ?, ?, ?) ON CONFLICT(key) DO NOTHING`,
			m.Key, m.Kind, m.FileName, m.Mime, m.Size); err != nil {
			return fmt.Errorf("save media %s: %w", m.Key, err)
		}
	}
	for _, m := range b.Messages {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO messages (chat_id, id, topic_id, date, from_id, text, entities_json, reply_to_id, fwd_from_json,
  grouped_id, reactions_json, service_action_json, pinned, edit_date, raw, tl_layer)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, id) DO NOTHING`,
			m.ChatID, m.ID, nullInt(int64(m.TopicID)), m.Date, nullInt(m.FromID), m.Text, nullStr(m.EntitiesJSON),
			nullInt(int64(m.ReplyToID)), nullStr(m.FwdFromJSON), nullInt(m.GroupedID), nullStr(m.ReactionsJSON),
			nullStr(m.ServiceActionJSON), m.Pinned, nullInt(m.EditDate), m.Raw, m.TLLayer); err != nil {
			return fmt.Errorf("save message %d/%d: %w", m.ChatID, m.ID, err)
		}
		for _, k := range m.MediaKeys {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO message_media (chat_id, message_id, media_key) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
				m.ChatID, m.ID, k); err != nil {
				return fmt.Errorf("link media %s to %d/%d: %w", k, m.ChatID, m.ID, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_cursors (chat_id, newest_id, oldest_id, backfill_done, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id) DO UPDATE SET newest_id = excluded.newest_id, oldest_id = excluded.oldest_id,
  backfill_done = excluded.backfill_done, updated_at = excluded.updated_at`,
		b.ChatID, b.Cursor.NewestID, b.Cursor.OldestID, b.Cursor.BackfillDone, now); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET last_synced_at = ? WHERE id = ?`, now, b.ChatID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetCursor(ctx context.Context, chatID int64) (Cursor, bool, error) {
	var c Cursor
	err := s.db.QueryRowContext(ctx,
		`SELECT newest_id, oldest_id, backfill_done FROM sync_cursors WHERE chat_id = ?`, chatID).
		Scan(&c.NewestID, &c.OldestID, &c.BackfillDone)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, err
	}
	return c, true, nil
}

func (s *Store) SetPinned(ctx context.Context, chatID int64, ids []int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET pinned = 0 WHERE chat_id = ? AND pinned = 1`, chatID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET pinned = 1 WHERE chat_id = ? AND id = ?`, chatID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PinnedMessageIDs(ctx context.Context, chatID int64) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE chat_id = ? AND pinned = 1 ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) PeerName(ctx context.Context, id int64) (Peer, bool, error) {
	p := Peer{ID: id}
	err := s.db.QueryRowContext(ctx, `SELECT title, username FROM peers WHERE id = ?`, id).Scan(&p.Title, &p.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return Peer{}, false, nil
	}
	if err != nil {
		return Peer{}, false, err
	}
	return p, true, nil
}

func (s *Store) CountMessages(ctx context.Context, chatID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE chat_id = ?`, chatID).Scan(&n)
	return n, err
}
