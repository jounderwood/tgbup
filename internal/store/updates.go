package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Update is a numbered change of the archive and the snapshots that hold it.
type Update struct {
	N         int
	At        time.Time
	Diff      UpdateDiff
	Snapshots []SnapshotRef
}

// UpdateDiff is what changed since the update before.
type UpdateDiff struct {
	// First: there is no update before; the counts are the whole archive.
	First    bool `json:"first,omitempty"`
	Messages int  `json:"messages"` // net: deleted chats count against it
	Files    int  `json:"files"`
	// Edited messages kept their version before; Updated ones got other
	// reactions, votes or a link preview.
	Edited  int `json:"edited,omitempty"`
	Updated int `json:"updated,omitempty"`
	// Deleted messages are gone from Telegram; the archive keeps them.
	Deleted int `json:"deleted,omitempty"`
	// Pages redrawn: edits, reactions, checklist ticks, a new tgbup's look.
	Pages int `json:"pages,omitempty"`
	// Chats is the net change of messages per chat.
	Chats map[int64]int `json:"chats,omitempty"`
}

func (d UpdateDiff) empty() bool {
	return d.Messages == 0 && d.Files == 0 && d.Edited == 0 && d.Updated == 0 && d.Deleted == 0 && d.Pages == 0 && len(d.Chats) == 0
}

type SnapshotRef struct {
	Repo, ID  string
	At        time.Time
	Partial   bool // some files could not be read
	Forgotten bool
}

type totals struct {
	Messages map[int64]int `json:"messages"`
	Files    int           `json:"files"`
}

// NoteUpdate records a new update when the archive changed since the last
// one: messages, downloaded files, edits, or pages redrawn (pages, from the
// render just done). It returns the latest update and whether it is new; ok
// is false while the archive is empty and has no update.
func (s *Store) NoteUpdate(ctx context.Context, pages int, now time.Time) (u Update, created, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Update{}, false, false, err
	}
	defer func() { _ = tx.Rollback() }()

	cur := totals{Messages: map[int64]int{}}
	rows, err := tx.QueryContext(ctx, `SELECT chat_id, count(*) FROM messages GROUP BY chat_id`)
	if err != nil {
		return Update{}, false, false, err
	}
	for rows.Next() {
		var chat int64
		var n int
		if err := rows.Scan(&chat, &n); err != nil {
			rows.Close()
			return Update{}, false, false, err
		}
		cur.Messages[chat] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Update{}, false, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM media WHERE status = 'done'`).Scan(&cur.Files); err != nil {
		return Update{}, false, false, err
	}

	var (
		last       Update
		lastTotals totals
		diffJSON   string
		totalsJSON string
		at         int64
	)
	err = tx.QueryRowContext(ctx, `SELECT n, at, diff_json, totals_json FROM updates ORDER BY n DESC LIMIT 1`).
		Scan(&last.N, &at, &diffJSON, &totalsJSON)
	hasLast := err == nil
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Update{}, false, false, err
	default:
		last.At = time.Unix(at, 0)
		_ = json.Unmarshal([]byte(diffJSON), &last.Diff)
		_ = json.Unmarshal([]byte(totalsJSON), &lastTotals)
	}

	d := UpdateDiff{First: !hasLast, Pages: pages, Files: cur.Files - lastTotals.Files, Chats: map[int64]int{}}
	for chat, n := range cur.Messages {
		if delta := n - lastTotals.Messages[chat]; delta != 0 {
			d.Chats[chat] = delta
			d.Messages += delta
		}
	}
	for chat, n := range lastTotals.Messages {
		if _, still := cur.Messages[chat]; !still {
			d.Chats[chat] = -n
			d.Messages -= n
		}
	}
	if hasLast {
		// What tgbup found changed since: edits kept a version, the rest got
		// other reactions, votes or a preview.
		var changed int
		if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM (SELECT DISTINCT chat_id, id FROM message_versions WHERE replaced_at > ?)),
  (SELECT count(*) FROM messages WHERE changed_at > ?),
  (SELECT count(*) FROM messages WHERE deleted_at > ?)`, at, at, at).Scan(&d.Edited, &changed, &d.Deleted); err != nil {
			return Update{}, false, false, err
		}
		d.Updated = max(changed-d.Edited, 0)
	}
	if !hasLast && len(cur.Messages) == 0 && cur.Files == 0 {
		return Update{}, false, false, nil
	}
	if hasLast && d.empty() {
		return last, false, true, nil
	}
	if !hasLast {
		d.Pages = 0 // the first drawing of everything is not news
	}
	dj, _ := json.Marshal(d)
	tj, _ := json.Marshal(cur)
	u = Update{N: last.N + 1, At: now, Diff: d}
	if _, err := tx.ExecContext(ctx, `INSERT INTO updates (n, at, diff_json, totals_json) VALUES (?, ?, ?, ?)`,
		u.N, now.Unix(), string(dj), string(tj)); err != nil {
		return Update{}, false, false, err
	}
	// Only the latest totals are compared with; with many chats they are big.
	if _, err := tx.ExecContext(ctx, `UPDATE updates SET totals_json = '{}' WHERE n < ?`, u.N); err != nil {
		return Update{}, false, false, err
	}
	return u, true, true, tx.Commit()
}

// Updates returns the latest updates, newest first, with their snapshots.
func (s *Store) Updates(ctx context.Context, limit int) ([]Update, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n, at, diff_json FROM updates ORDER BY n DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	at := map[int]int{}
	for rows.Next() {
		var (
			u    Update
			t    int64
			diff string
		)
		if err := rows.Scan(&u.N, &t, &diff); err != nil {
			return nil, err
		}
		u.At = time.Unix(t, 0)
		_ = json.Unmarshal([]byte(diff), &u.Diff)
		at[u.N] = len(out)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	srows, err := s.db.QueryContext(ctx, `SELECT repo, id, update_n, at, partial, forgotten_at IS NOT NULL FROM snapshots
WHERE update_n >= ? ORDER BY at, repo`, out[len(out)-1].N)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var (
			r SnapshotRef
			n int
			t int64
		)
		if err := srows.Scan(&r.Repo, &r.ID, &n, &t, &r.Partial, &r.Forgotten); err != nil {
			return nil, err
		}
		r.At = time.Unix(t, 0)
		if i, ok := at[n]; ok {
			out[i].Snapshots = append(out[i].Snapshots, r)
		}
	}
	return out, srows.Err()
}

// LastUpdate is the latest update; ok is false when there is none.
func (s *Store) LastUpdate(ctx context.Context) (Update, bool, error) {
	us, err := s.Updates(ctx, 1)
	if err != nil || len(us) == 0 {
		return Update{}, false, err
	}
	return us[0], true, nil
}

// AddSnapshot notes that repo got snapshot id holding update n; partial: some
// files could not be read into it.
func (s *Store) AddSnapshot(ctx context.Context, repo, id string, n int, partial bool, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO snapshots (repo, id, update_n, at, partial) VALUES (?, ?, ?, ?, ?)`,
		repo, id, n, at.Unix(), partial)
	return err
}

// LastSnapshot is repo's newest full snapshot that retention has not removed,
// and the update it holds.
func (s *Store) LastSnapshot(ctx context.Context, repo string) (id string, n int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT id, update_n FROM snapshots WHERE repo = ? AND forgotten_at IS NULL AND NOT partial
ORDER BY at DESC, update_n DESC LIMIT 1`, repo).Scan(&id, &n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return id, n, err == nil, err
}

// ForgetSnapshots notes that retention removed these snapshots from repo.
func (s *Store) ForgetSnapshots(ctx context.Context, repo string, ids []string, at time.Time) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE snapshots SET forgotten_at = ? WHERE repo = ? AND id = ? AND forgotten_at IS NULL`,
			at.Unix(), repo, id); err != nil {
			return err
		}
	}
	return nil
}

// SnapshotUpdates maps repo's snapshot ids to the updates they hold.
func (s *Store) SnapshotUpdates(ctx context.Context, repo string) (map[string]Update, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, u.n, u.at, u.diff_json FROM snapshots s JOIN updates u ON u.n = s.update_n
WHERE s.repo = ?`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Update{}
	for rows.Next() {
		var (
			id, diff string
			u        Update
			t        int64
		)
		if err := rows.Scan(&id, &u.N, &t, &diff); err != nil {
			return nil, err
		}
		u.At = time.Unix(t, 0)
		_ = json.Unmarshal([]byte(diff), &u.Diff)
		out[id] = u
	}
	return out, rows.Err()
}

// ForgottenSnapshots lists repo's snapshots that retention removed.
func (s *Store) ForgottenSnapshots(ctx context.Context, repo string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM snapshots WHERE repo = ? AND forgotten_at IS NOT NULL ORDER BY at`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Summary says the diff in a line: "+120 messages · +5 files · 3 edited".
func (d UpdateDiff) Summary() string {
	if d.First {
		return fmt.Sprintf("first recorded: %s, %s", countOf(d.Messages, "message"), countOf(d.Files, "file"))
	}
	var parts []string
	if d.Messages != 0 {
		parts = append(parts, signed(d.Messages, "message"))
	}
	if d.Files != 0 {
		parts = append(parts, signed(d.Files, "file"))
	}
	if d.Edited != 0 {
		parts = append(parts, fmt.Sprintf("%d edited", d.Edited))
	}
	if d.Updated != 0 {
		parts = append(parts, fmt.Sprintf("%d with new reactions or votes", d.Updated))
	}
	if d.Deleted != 0 {
		parts = append(parts, fmt.Sprintf("%d deleted in Telegram", d.Deleted))
	}
	if d.Pages != 0 {
		parts = append(parts, countOf(d.Pages, "page")+" redrawn")
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, " · ")
}

func countOf(n int, noun string) string {
	if n == 1 || n == -1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func signed(n int, noun string) string {
	if n > 0 {
		return "+" + countOf(n, noun)
	}
	return countOf(n, noun)
}

// SnapshotsByUpdate lists the kept snapshots of every place by the update
// they hold: one version of the archive in several places.
func (s *Store) SnapshotsByUpdate(ctx context.Context) (map[int][]SnapshotRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT update_n, repo, id, at, partial FROM snapshots
WHERE forgotten_at IS NULL ORDER BY at, repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int][]SnapshotRef{}
	for rows.Next() {
		var (
			n int
			r SnapshotRef
			t int64
		)
		if err := rows.Scan(&n, &r.Repo, &r.ID, &t, &r.Partial); err != nil {
			return nil, err
		}
		r.At = time.Unix(t, 0)
		out[n] = append(out[n], r)
	}
	return out, rows.Err()
}

// MessageVersion is a version of a message from before an edit.
type MessageVersion struct {
	N            int
	Text         string
	EntitiesJSON string
	MediaKeys    []string
	EditDate     int64 // Telegram's time of this version; 0: as sent
	ReplacedAt   int64 // when tgbup found the next one
}

// MessageVersions lists the earlier versions of the chat's messages ids,
// oldest first.
func (s *Store) MessageVersions(ctx context.Context, chatID int64, ids []int) (map[int][]MessageVersion, error) {
	out := map[int][]MessageVersion{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{chatID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, n, text, COALESCE(entities_json, ''), COALESCE(media_keys, ''),
       COALESCE(edit_date, 0), replaced_at
FROM message_versions WHERE chat_id = ? AND id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY id, n`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int
			v    MessageVersion
			keys string
		)
		if err := rows.Scan(&id, &v.N, &v.Text, &v.EntitiesJSON, &keys, &v.EditDate, &v.ReplacedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(keys), &v.MediaKeys)
		out[id] = append(out[id], v)
	}
	return out, rows.Err()
}
