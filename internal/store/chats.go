package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

func (s *Store) UpsertChat(ctx context.Context, c Chat) error {
	if c.Folders == nil {
		c.Folders = []string{}
	}
	folders, err := json.Marshal(c.Folders)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO chats (id, type, title, username, folders_json, migrated_from_id, status, first_seen_at)
VALUES (?, ?, ?, ?, ?, ?, 'active', ?)
ON CONFLICT(id) DO UPDATE SET
  type = excluded.type,
  title = excluded.title,
  username = excluded.username,
  folders_json = excluded.folders_json,
  migrated_from_id = COALESCE(excluded.migrated_from_id, chats.migrated_from_id)`,
		c.ID, c.Type, c.Title, c.Username, string(folders), nullInt(c.MigratedFromID), time.Now().Unix())
	return err
}

func (s *Store) Chat(ctx context.Context, id int64) (Chat, bool, error) {
	var (
		c       Chat
		folders string
		migr    sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, type, title, username, folders_json, migrated_from_id, status FROM chats WHERE id = ?`, id).
		Scan(&c.ID, &c.Type, &c.Title, &c.Username, &folders, &migr, &c.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, err
	}
	c.MigratedFromID = migr.Int64
	if err := json.Unmarshal([]byte(folders), &c.Folders); err != nil {
		return Chat{}, false, err
	}
	return c, true, nil
}

func (s *Store) ActiveChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM chats WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) MarkInaccessible(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET status = 'inaccessible' WHERE id = ? AND status = 'active'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) MarkActive(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET status = 'active' WHERE id = ?`, id)
	return err
}

func (s *Store) UpsertTopics(ctx context.Context, topics []Topic) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range topics {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO topics (chat_id, topic_id, title, closed, created_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id, topic_id) DO UPDATE SET title = excluded.title, closed = excluded.closed`,
			t.ChatID, t.TopicID, t.Title, t.Closed, t.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Topics(ctx context.Context, chatID int64) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT chat_id, topic_id, title, closed, created_at FROM topics WHERE chat_id = ? ORDER BY topic_id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topic
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ChatID, &t.TopicID, &t.Title, &t.Closed, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ApplyTopicExclusions records the forum's excluded topics. When a previously
// excluded topic that still exists is no longer excluded, its messages were
// skipped so far, so the backfill restarts from the newest message and reads
// the history again.
func (s *Store) ApplyTopicExclusions(ctx context.Context, chatID int64, excluded, existing []int) (reset bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var prevJSON string
	if err := tx.QueryRowContext(ctx, `SELECT excluded_topics_json FROM chats WHERE id = ?`, chatID).Scan(&prevJSON); err != nil {
		return false, fmt.Errorf("read topic exclusions of %d: %w", chatID, err)
	}
	var prev []int
	if err := json.Unmarshal([]byte(prevJSON), &prev); err != nil {
		return false, err
	}
	next := slices.Sorted(slices.Values(excluded))
	for _, id := range prev {
		if !slices.Contains(next, id) && slices.Contains(existing, id) {
			reset = true
			break
		}
	}
	if reset {
		res, err := tx.ExecContext(ctx,
			`UPDATE sync_cursors SET oldest_id = newest_id + 1, backfill_done = 0, updated_at = ? WHERE chat_id = ?`,
			time.Now().Unix(), chatID)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return false, err
		} else if n == 0 {
			reset = false
		}
	}
	if next == nil {
		next = []int{}
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET excluded_topics_json = ? WHERE id = ?`, string(nextJSON), chatID); err != nil {
		return false, err
	}
	return reset, tx.Commit()
}
