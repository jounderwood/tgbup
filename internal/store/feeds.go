package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Feed is where one of Telegram's change feeds stands. Box 0 is the
// account's own feed (private chats and basic groups); any other box is the
// chat id of a channel or supergroup, which has a feed of its own.
type Feed struct {
	Box            int64
	Pts, Qts, Date int
	ReadAt         int64 // when a run last read it; 0: never
}

// Feeds lists the feeds followed, by box.
func (s *Store) Feeds(ctx context.Context) (map[int64]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT box, pts, qts, date, read_at FROM feeds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Feed{}
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.Box, &f.Pts, &f.Qts, &f.Date, &f.ReadAt); err != nil {
			return nil, err
		}
		out[f.Box] = f
	}
	return out, rows.Err()
}

// Feed returns where a feed stands; false: it is not followed yet.
func (s *Store) Feed(ctx context.Context, box int64) (Feed, bool, error) {
	f := Feed{Box: box}
	err := s.db.QueryRowContext(ctx, `SELECT pts, qts, date, read_at FROM feeds WHERE box = ?`, box).Scan(&f.Pts, &f.Qts, &f.Date, &f.ReadAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, false, nil
	}
	if err != nil {
		return Feed{}, false, err
	}
	return f, true, nil
}

// SetFeed records where a feed stands and when it was read.
func (s *Store) SetFeed(ctx context.Context, f Feed) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO feeds (box, pts, qts, date, read_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(box) DO UPDATE SET pts = excluded.pts, qts = excluded.qts, date = excluded.date, read_at = excluded.read_at`,
		f.Box, f.Pts, f.Qts, f.Date, f.ReadAt)
	return err
}

// StartFeed starts following a feed from where it stands, unless it is
// followed already; it was never read.
func (s *Store) StartFeed(ctx context.Context, f Feed) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO feeds (box, pts, qts, date) VALUES (?, ?, ?, ?)
ON CONFLICT(box) DO NOTHING`, f.Box, f.Pts, f.Qts, f.Date)
	return err
}

// ForgetFeed stops following a feed whose place is lost; it starts again
// from where it next stands.
func (s *Store) ForgetFeed(ctx context.Context, box int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM feeds WHERE box = ?`, box)
	return err
}

// MessageState is what the archive knows of a message for a change feed.
type MessageState struct {
	EditDate int64 // Telegram's time of the archived version; 0: as sent
	Deleted  bool  // marked deleted in Telegram
}

// MessageStates gives the archived messages among the ids, by id.
func (s *Store) MessageStates(ctx context.Context, chatID int64, ids []int) (map[int]MessageState, error) {
	out := map[int]MessageState{}
	err := inChunks(ids, func(in string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(edit_date, 0), deleted_at IS NOT NULL FROM messages
WHERE chat_id = ? AND id IN (`+in+`)`, append([]any{chatID}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var m MessageState
			if err := rows.Scan(&id, &m.EditDate, &m.Deleted); err != nil {
				return err
			}
			out[id] = m
		}
		return rows.Err()
	})
	return out, err
}

// CommonChats finds the archived messages with these ids in private chats
// and basic groups, not marked deleted: chat → ids. The account numbers the
// messages of all those chats in one sequence, so an id names one message.
// (CROSS JOIN keeps SQLite going chat by chat on the primary key instead of
// reading every message.)
func (s *Store) CommonChats(ctx context.Context, ids []int) (map[int64][]int, error) {
	out := map[int64][]int{}
	err := inChunks(ids, func(in string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT m.chat_id, m.id FROM chats c
CROSS JOIN messages m ON m.chat_id = c.id AND m.id IN (`+in+`)
WHERE c.type IN (?, ?) AND m.deleted_at IS NULL ORDER BY m.chat_id, m.id`, append(args, ChatPrivate, ChatGroup)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var chat int64
			var id int
			if err := rows.Scan(&chat, &id); err != nil {
				return err
			}
			out[chat] = append(out[chat], id)
		}
		return rows.Err()
	})
	return out, err
}

// inChunks calls fn with "?,?,…" and its arguments for the ids, 500 at a
// time: SQLite caps the arguments of a statement.
func inChunks(ids []int, fn func(in string, args []any) error) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = id
		}
		if err := fn("?"+strings.Repeat(",?", n-1), args); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}
