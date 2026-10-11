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

// SaveStats says what a batch changed in the archive.
type SaveStats struct {
	New int // messages the archive did not have
	// Edited: the text, its formatting or the attachment changed; the
	// version before is kept in message_versions.
	Edited int
	// Updated: reactions, a poll's votes, a link preview or a checklist
	// changed; kept current, without versions.
	Updated int
}

func (s *Store) SaveBatch(ctx context.Context, b Batch) error {
	_, err := s.SaveBatchStats(ctx, b)
	return err
}

// SaveBatchStats saves a batch and tells what it changed: a message read
// again from Telegram replaces the archived one when it differs, keeping the
// version before when it was edited.
func (s *Store) SaveBatchStats(ctx context.Context, b Batch) (SaveStats, error) {
	var stats SaveStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
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
			return stats, fmt.Errorf("save user %d: %w", u.ID, err)
		}
	}
	for _, p := range b.Peers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO peers (id, title, username, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET title = excluded.title, username = excluded.username, updated_at = excluded.updated_at`,
			p.ID, p.Title, p.Username, now); err != nil {
			return stats, fmt.Errorf("save peer %d: %w", p.ID, err)
		}
	}
	for _, m := range b.Media {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO media (key, kind, file_name, mime, size) VALUES (?, ?, ?, ?, ?) ON CONFLICT(key) DO NOTHING`,
			m.Key, m.Kind, m.FileName, m.Mime, m.Size); err != nil {
			return stats, fmt.Errorf("save media %s: %w", m.Key, err)
		}
	}
	for _, m := range b.Messages {
		res, err := tx.ExecContext(ctx, `
INSERT INTO messages (chat_id, id, topic_id, date, from_id, text, entities_json, reply_to_id, fwd_from_json,
  grouped_id, reactions_json, service_action_json, pinned, edit_date, raw, tl_layer, meta_json, checked_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, id) DO NOTHING`,
			m.ChatID, m.ID, nullInt(int64(m.TopicID)), m.Date, nullInt(m.FromID), m.Text, nullStr(m.EntitiesJSON),
			nullInt(int64(m.ReplyToID)), nullStr(m.FwdFromJSON), nullInt(m.GroupedID), nullStr(m.ReactionsJSON),
			nullStr(m.ServiceActionJSON), m.Pinned, nullInt(m.EditDate), m.Raw, m.TLLayer, nullStr(m.MetaJSON), now)
		if err != nil {
			return stats, fmt.Errorf("save message %d/%d: %w", m.ChatID, m.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			stats.New++
			if err := linkMedia(ctx, tx, m); err != nil {
				return stats, err
			}
			continue
		}
		change, err := refreshMessage(ctx, tx, m, now)
		if err != nil {
			return stats, fmt.Errorf("update message %d/%d: %w", m.ChatID, m.ID, err)
		}
		switch change {
		case changeEdited:
			stats.Edited++
		case changeUpdated:
			stats.Updated++
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_cursors (chat_id, newest_id, oldest_id, backfill_done, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id) DO UPDATE SET newest_id = excluded.newest_id, oldest_id = excluded.oldest_id,
  backfill_done = excluded.backfill_done, updated_at = excluded.updated_at`,
		b.ChatID, b.Cursor.NewestID, b.Cursor.OldestID, b.Cursor.BackfillDone, now); err != nil {
		return stats, fmt.Errorf("save cursor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET last_synced_at = ? WHERE id = ?`, now, b.ChatID); err != nil {
		return stats, err
	}
	return stats, tx.Commit()
}

func linkMedia(ctx context.Context, tx *sql.Tx, m Message) error {
	for _, k := range m.MediaKeys {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO message_media (chat_id, message_id, media_key) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			m.ChatID, m.ID, k); err != nil {
			return fmt.Errorf("link media %s to %d/%d: %w", k, m.ChatID, m.ID, err)
		}
	}
	return nil
}

type change int

const (
	changeNone change = iota
	changeUpdated
	changeEdited
)

// refreshMessage brings an archived message up to the version just read
// from Telegram. An edit — other text or attachment, or other formatting with
// a new edit time — keeps the version before; reactions, votes, a link
// preview or a checklist are replaced in place. Formatting that differs
// without an edit (tgbup storing it another way) is no edit either. The pin
// is SetPinned's.
func refreshMessage(ctx context.Context, tx *sql.Tx, m Message, now int64) (change, error) {
	var (
		text, ents, meta, reacts string
		edit                     int64
	)
	if err := tx.QueryRowContext(ctx, `SELECT text, COALESCE(entities_json, ''), COALESCE(meta_json, ''),
       COALESCE(reactions_json, ''), COALESCE(edit_date, 0) FROM messages WHERE chat_id = ? AND id = ?`, m.ChatID, m.ID).
		Scan(&text, &ents, &meta, &reacts, &edit); err != nil {
		return changeNone, err
	}
	oldKeys, err := mediaKeys(ctx, tx, m.ChatID, m.ID)
	if err != nil {
		return changeNone, err
	}
	newKeys := slices.Sorted(slices.Values(m.MediaKeys))
	edited := text != m.Text || !slices.Equal(oldKeys, newKeys) || (ents != m.EntitiesJSON && edit != m.EditDate)
	updated := ents != m.EntitiesJSON || meta != m.MetaJSON || reacts != m.ReactionsJSON || edit != m.EditDate
	switch {
	case edited:
		keys, _ := json.Marshal(oldKeys)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO message_versions (chat_id, id, n, text, entities_json, media_keys, edit_date, replaced_at)
VALUES (?, ?, (SELECT COALESCE(MAX(n), 0) + 1 FROM message_versions WHERE chat_id = ? AND id = ?), ?, ?, ?, ?, ?)`,
			m.ChatID, m.ID, m.ChatID, m.ID, text, nullStr(ents), string(keys), nullInt(edit), now); err != nil {
			return changeNone, err
		}
		if !slices.Equal(oldKeys, newKeys) {
			// The attachment was replaced: the page shows the new one; the old
			// file stays in media/, named in the version before.
			if _, err := tx.ExecContext(ctx, `DELETE FROM message_media WHERE chat_id = ? AND message_id = ?`, m.ChatID, m.ID); err != nil {
				return changeNone, err
			}
			if err := linkMedia(ctx, tx, m); err != nil {
				return changeNone, err
			}
		}
	case !updated:
		// Read from Telegram: it is there, whatever an earlier check thought.
		_, err := tx.ExecContext(ctx, `UPDATE messages SET checked_at = ?, deleted_at = NULL WHERE chat_id = ? AND id = ?`, now, m.ChatID, m.ID)
		return changeNone, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE messages SET text = ?, entities_json = ?, meta_json = ?, reactions_json = ?, edit_date = ?, raw = ?, tl_layer = ?,
  changed_at = ?, checked_at = ?, deleted_at = NULL
WHERE chat_id = ? AND id = ?`,
		m.Text, nullStr(m.EntitiesJSON), nullStr(m.MetaJSON), nullStr(m.ReactionsJSON), nullInt(m.EditDate), m.Raw, m.TLLayer,
		now, now, m.ChatID, m.ID); err != nil {
		return changeNone, err
	}
	if edited {
		return changeEdited, nil
	}
	return changeUpdated, nil
}

func mediaKeys(ctx context.Context, tx *sql.Tx, chat int64, id int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT media_key FROM message_media WHERE chat_id = ? AND message_id = ? ORDER BY media_key`, chat, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
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

// MessageTotal counts the messages of every chat.
func (s *Store) MessageTotal(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&n)
	return n, err
}

func (s *Store) CountMessages(ctx context.Context, chatID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE chat_id = ?`, chatID).Scan(&n)
	return n, err
}

// LatestMessageDate is the date of the chat's newest archived message.
func (s *Store) LatestMessageDate(ctx context.Context, chatID int64) (int64, bool, error) {
	var d sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(date) FROM messages WHERE chat_id = ?`, chatID).Scan(&d)
	return d.Int64, d.Valid, err
}

// ArchivedIDs lists the chat's archived messages with ids from lo to hi,
// inclusive, that are not marked deleted.
func (s *Store) ArchivedIDs(ctx context.Context, chatID int64, lo, hi int) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE chat_id = ? AND id BETWEEN ? AND ? AND deleted_at IS NULL ORDER BY id`,
		chatID, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkDeleted notes that the messages are gone from Telegram; they stay in
// the archive. It returns how many were not marked before.
func (s *Store) MarkDeleted(ctx context.Context, chatID int64, ids []int, at int64) (int, error) {
	n := 0
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, `UPDATE messages SET deleted_at = ? WHERE chat_id = ? AND id = ? AND deleted_at IS NULL`, at, chatID, id)
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, nil
}

// Audit is where a chat's audit stands.
type Audit struct {
	ChatID    int64
	BeforeID  int   // the current round goes on below this id; 0: from the newest
	AuditedAt int64 // when the last full round ended; 0: never
}

// Audits lists the chats whose first download is done, the least recently
// audited first: the next run goes on with them.
func (s *Store) Audits(ctx context.Context) ([]Audit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, audit_before_id, COALESCE(audited_at, 0) FROM sync_cursors
WHERE backfill_done ORDER BY COALESCE(audited_at, 0), chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var a Audit
		if err := rows.Scan(&a.ChatID, &a.BeforeID, &a.AuditedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAudit records where a chat's audit stands.
func (s *Store) SetAudit(ctx context.Context, a Audit) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_cursors SET audit_before_id = ?, audited_at = NULLIF(?, 0) WHERE chat_id = ?`,
		a.BeforeID, a.AuditedAt, a.ChatID)
	return err
}
